package workernode

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/dariopb/netd/pkg/helpers"
	"github.com/dariopb/netd/pkg/reconciler"
	types "github.com/dariopb/netd/pkg/types"
	pb "github.com/dariopb/netd/proto/netd.v1alpha"
	pbNetNode "github.com/dariopb/netd/proto/netnode.v1alpha"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	log "github.com/sirupsen/logrus"
)

type WorkerNode struct {
	NodeID      string
	port        int
	isNodeStack bool
	outboundIP  string
	conn        *grpc.ClientConn
	vnetClient  pb.NetworkDClient
	grpcTimeout time.Duration

	vrf      types.NetworkNodeOps
	basePort int

	nicWatcherMap map[string]*NicWatcher
	nicWatcherMtx sync.Mutex

	sd    *localNodeData
	store *helpers.ConfigFile

	ctx      context.Context
	cancelFn context.CancelFunc
}

type NicCallbackFunc func(string, string, *pb.WatchIPMappingResponse)

type NicWatcher struct {
	vnetID      string
	nicEventMap map[string]*pb.WatchIPMappingResponse

	ctx      context.Context
	cancelFn context.CancelFunc
	cbMap    map[string]NicCallbackFunc
}

// localNodeData holds local subnet/nic data mapping
type localNodeData struct {
	Name     string              `json:"name"`
	VlanPool *helpers.SimplePool `json:"vlanPool"`

	Nics    map[string]*types.NicData `json:"nics"`
	Vnets   map[string]*localVnetData `json:"vnets"`
	VlanMap map[string]string         `json:"vlanMap"`
	mtx     sync.Mutex
}

type localVnetData struct {
	Vnet *pb.VNet                        `json:"vnet"`
	Nics map[string]*pb.NicConfiguration `json:"nicMap"`
}

// NewWorkerNode creates a worker node
func NewWorkerNode(nodeID string, port int, conn *grpc.ClientConn,
	defaultSubnet string, isNodeStack bool, cleanup bool) (*WorkerNode, error) {
	var err error

	log.Infof("Creating workerNode: nodeID: %s, grpc port: %d, defaultSubnet: %s",
		nodeID, port, defaultSubnet)

	outboundIP := getOutboundIP()

	//ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	netClient := pb.NewNetworkDClient(conn)

	wn := &WorkerNode{
		NodeID:        nodeID,
		port:          port,
		conn:          conn,
		grpcTimeout:   5 * time.Second,
		vnetClient:    netClient,
		isNodeStack:   isNodeStack,
		outboundIP:    outboundIP,
		nicWatcherMap: make(map[string]*NicWatcher),
		basePort:      44000,
	}

	if isNodeStack {
		wn.vrf = vmVRF{}
		//wn.vrf = nodeVRF{
		//	NodeName:     nodeID,
		//	IsNamespaced: false,
		//}
	} else {
		//wn.vrf = containerVRF{}
	}

	// Initialize/retrieve the subnet runtime data
	sd := &localNodeData{Name: "localNodeData"}
	wn.store, err = helpers.NewAppData("local_node_store.json")
	wn.store.LoadConfig(sd, false, func() (interface{}, error) {
		sd.Nics = make(map[string]*types.NicData)
		sd.Vnets = make(map[string]*localVnetData)
		sd.VlanMap = make(map[string]string)

		pool := helpers.NewSimplePool("vlanpool")

		min := 100
		max := 120
		vlans := make([]string, max-min+1)
		for i := range vlans {
			vlans[i] = fmt.Sprintf("%d", min+i)
		}
		pool.InitializeElements(vlans)
		sd.VlanPool = pool

		return sd, nil
	})
	if err != nil {
		return nil, err
	}

	// Clean up the fresh data
	// Notice that I need to preserve at least the vlan pool and the vlan<->subnet map!
	sd.Nics = make(map[string]*types.NicData)
	//sd.Vnets = make(map[string]*localVnetData)

	// keep it handy
	wn.sd = sd
	wn.store.Save()

	wn.startLocalServer()

	// Register the local state
	for _, localVnet := range wn.sd.Vnets {
		for _, nic := range localVnet.Nics {
			vlanID, err := wn.sd.VlanPool.QueryItem(nic.Name)
			if err != nil {
				log.Errorf("Nic: [%s] doesn't have a matching local state!. Skipping", nic.Name)
				continue
			}

			err = wn.setupLocalEndpoint(localVnet.Vnet, nic, vlanID)
			if err != nil {
				log.Errorf("Nic: [%s] failed to setup during startup", nic.Name)
			}
		}
	}

	go wn.nodeLoop()

	return wn, nil
}

func (wn *WorkerNode) getVlanIDAndPersist(nicID string) (string, error) {
	wn.sd.mtx.Lock()
	defer wn.sd.mtx.Unlock()

	var err error

	vlanId, ok := wn.sd.VlanMap[nicID]
	if !ok {
		vlanId, err = wn.sd.VlanPool.GetItem(nicID, nicID, nil)
		if err != nil {
			return "", err
		}

		wn.sd.VlanMap[nicID] = vlanId
	}

	wn.store.Save()

	return vlanId, err
}

func (wn *WorkerNode) getVlanIDAllocated(nicID string) string {
	wn.sd.mtx.Lock()
	defer wn.sd.mtx.Unlock()

	vlanId := wn.sd.VlanMap[nicID]

	return vlanId
}

func (wn *WorkerNode) releaseVlanIDAndPersist(nicID string) error {
	wn.sd.mtx.Lock()
	defer wn.sd.mtx.Unlock()

	var err error

	_, ok := wn.sd.VlanMap[nicID]
	if ok {
		_, err = wn.sd.VlanPool.ReleaseItem(nicID, nil)
		if err != nil {
			return err
		}

		delete(wn.sd.VlanMap, nicID)
		wn.store.Save()
	}

	return nil
}

// startLocalServer starts the local grpc server for the VMs/containers/node.
func (wn *WorkerNode) startLocalServer() {
	var opts []grpc.ServerOption

	ws, err := NewWorkerServer(wn)
	if err != nil {
		log.Fatalf("Failed to create impl: %v", err)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", wn.port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer(opts...)
	pbNetNode.RegisterNetNodeServer(grpcServer, ws)
	reflection.Register(grpcServer)

	go func() {
		grpcServer.Serve(lis)
		if err != nil {
			log.Fatal(err)
		}
	}()

}

// Get outbound ip of this machine
func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:8888")
	if err != nil {
		return ""
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String()
}

func (wn *WorkerNode) nodeLoop() {

	//key := fmt.Sprintf("servers/%s/nics", wn.NodeID)
	wn.ctx, wn.cancelFn = context.WithCancel(context.Background())
	//wn.client.SubscribeResource(wn.ctx, key, true, &types.NicData{}, wn.nicChanged)

}

func (wn *WorkerNode) watchVnetNicsLoopFull(vnetID string, nicID string, cb NicCallbackFunc) error {
	wn.nicWatcherMtx.Lock()
	defer wn.nicWatcherMtx.Unlock()

	if nw, ok := wn.nicWatcherMap[vnetID]; ok {
		nw.cbMap[nicID] = cb

		// here need to apply all the nics event that accumulated for the vnet
		for _, ipMap := range nw.nicEventMap {
			cb(nw.vnetID, nicID, ipMap)
		}

		return nil
	}

	// Create a new watcher
	ctx, cancelFn := context.WithCancel(context.Background())

	nw := &NicWatcher{
		vnetID:      vnetID,
		ctx:         ctx,
		cancelFn:    cancelFn,
		cbMap:       make(map[string]NicCallbackFunc),
		nicEventMap: make(map[string]*pb.WatchIPMappingResponse),
	}

	wn.nicWatcherMap[vnetID] = nw
	nw.cbMap[nicID] = cb

	go wn.watchVNetNicsLoop(nw)
	go wn.watchServiceLoop(nw.ctx)

	return nil
}

func (wn *WorkerNode) stopWatchVnetNics(vnetID string, nicID string) {
	wn.nicWatcherMtx.Lock()
	defer wn.nicWatcherMtx.Unlock()

	if nw, ok := wn.nicWatcherMap[vnetID]; ok {
		delete(nw.cbMap, nicID)

		if len(nw.cbMap) == 0 {
			if nw.cancelFn != nil {
				nw.cancelFn()
			}

			delete(wn.nicWatcherMap, vnetID)
		}

		return
	}
}

func WatchIPMappingResponse_GeyKey(a *pb.WatchIPMappingResponse) (string, reconciler.DeltaType) {
	return a.Key, (reconciler.DeltaType)(a.Action)
}

func WatchIPMappingResponse_AreEqual(a *pb.WatchIPMappingResponse, b *pb.WatchIPMappingResponse) bool {
	return a.Id == b.Id && a.Key == b.Key
}

func (wn *WorkerNode) watchVNetNicsLoop(nw *NicWatcher) error {

	m := make(map[string]*pb.WatchIPMappingResponse)

	w, err := reconciler.NewStreamWatcherWithState[pb.NetworkD_WatchIPMappingClient, *pb.WatchIPMappingResponse](2*time.Second,
		WatchIPMappingResponse_GeyKey, WatchIPMappingResponse_AreEqual,
		&m, &wn.nicWatcherMtx)
	if err != nil {
		return err
	}

	err = w.WatchLoop("watchVNetNicsLoop",
		func(ctx context.Context) (pb.NetworkD_WatchIPMappingClient, error) {
			r := &pb.WatchIPMappingRequest{
				VnetID: nw.vnetID,
			}

			l := log.WithField("vnet", nw.vnetID)
			l.Infof("Starting subscription for IP mapping")

			watchIP, err := wn.vnetClient.WatchIPMapping(ctx, r)
			if err != nil {
				return nil, err
			}
			return watchIP, nil
		},
		func(ipMap *pb.WatchIPMappingResponse) error {
			// TODO: dario do better than this :)
			wn.nicWatcherMtx.Lock()

			if ipMap.Action == pb.WatchIPMappingResponse_DELETE {
				// prefer the stored one so I have the data...
				ipMap = nw.nicEventMap[ipMap.Key]
				ipMap.Action = pb.WatchIPMappingResponse_DELETE
			} else {
				nw.nicEventMap[ipMap.Key] = ipMap
			}

			for nicID, cb := range nw.cbMap {
				cb(nw.vnetID, nicID, ipMap)
			}

			if ipMap.Action == pb.WatchIPMappingResponse_DELETE {
				delete(nw.nicEventMap, ipMap.Key)
			}

			wn.nicWatcherMtx.Unlock()
			return nil
		},
		nw.ctx)

	return nil
}

func (wn *WorkerNode) setupLocalEndpoint(vnet *pb.VNet, nic *pb.NicConfiguration, localIndex string) error {
	log.Infof("setupLocalEndpoint: setting up infra for: vnet: [%s], nic: [%s] with ip: [%s]", vnet.Id, nic.Name, nic.IPConfiguration.IPAddress)

	_, err := wn.vrf.SetupVRF(vnet, nic, localIndex)
	if err != nil {
		return err
	}

	// Persist the fact that I have local state
	wn.sd.mtx.Lock()
	var localVnet *localVnetData
	ok := false
	if localVnet, ok = wn.sd.Vnets[vnet.Id]; !ok {
		localVnet = &localVnetData{
			Nics: make(map[string]*pb.NicConfiguration, 0),
			Vnet: vnet,
		}
	}
	localVnet.Nics[nic.Name] = nic
	wn.sd.Vnets[vnet.Id] = localVnet
	wn.store.Save()
	wn.sd.mtx.Unlock()

	// start watching ip mappings on this vnet
	wn.watchVnetNicsLoopFull(vnet.Id, nic.Name, wn.NicChanged)

	return nil
}

func (wn *WorkerNode) removeLocalEndpoint(vnetID string, nicID string) error {
	log.Infof("removeLocalEndpoint: removing infra for: vnet: [%s], nic: [%s]", vnetID, nicID)

	wn.stopWatchVnetNics(vnetID, nicID)

	localIndex := wn.getVlanIDAllocated(nicID)
	err := wn.vrf.CleanupVRF(vnetID, nicID, localIndex)
	if err != nil {
		return err
	}

	err = wn.releaseVlanIDAndPersist(nicID)
	if err != nil {
		return err
	}

	// Persist the fact that I have local state
	wn.sd.mtx.Lock()
	localVnet, ok := wn.sd.Vnets[vnetID]

	if ok {
		delete(localVnet.Nics, nicID)

		if len(localVnet.Nics) == 0 {
			delete(wn.sd.Vnets, vnetID)
		}
		wn.store.Save()
	}
	wn.sd.mtx.Unlock()

	return nil
}

func (wn *WorkerNode) NicChanged(vnetID string, nicId string, data *pb.WatchIPMappingResponse) {
	if !strings.Contains(data.Key, nicId) {
		if data.Action == pb.WatchIPMappingResponse_DELETE {
			wn.vrf.RemoveEndpoint(vnetID, nicId, data.NicConfiguration)
		} else {
			wn.vrf.AddEndpoint(vnetID, nicId, data.NicConfiguration)
		}
	}
}
