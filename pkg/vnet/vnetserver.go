package vnetserver

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	badger "github.com/dgraph-io/badger/v3"

	"google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	status "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dariopb/netd/pkg/helpers"

	pb "github.com/dariopb/netd/proto/netd.v1alpha"

	log "github.com/sirupsen/logrus"
)

type SubscriptionChannel struct {
	ID      string
	Channel chan *pb.WatchVNetsResponse
}

type SubscriptionChannelNic struct {
	ID      string
	Channel chan *pb.WatchIPMappingResponse
}

type VNetServer struct {
	pb.UnimplementedNetworkDServer

	db *badger.DB
	//jsonMarsh   protojson.Marshaler
	//jsonUnMarsh jsonpb.Unmarshaler
	vnets map[string]*pb.VNet

	clientChannels    map[string]SubscriptionChannel
	clientChannelsNic map[string]SubscriptionChannelNic
	mtx               sync.Mutex
}

func NewVNetServer(dbFilepath string, grpcServer *grpc.Server) (*VNetServer, error) {
	opts := badger.DefaultOptions(dbFilepath).WithValueLogFileSize(50 * 1024 * 1024)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	/*jsonMarsh := jsonpb.Marshaler{
		EnumsAsInts:  false,
		EmitDefaults: true,
		Indent:       "  ",
		OrigName:     true,
	}*/

	n := &VNetServer{
		db:                db,
		clientChannels:    make(map[string]SubscriptionChannel),
		clientChannelsNic: make(map[string]SubscriptionChannelNic),
	}

	if grpcServer != nil {
		pb.RegisterNetworkDServer(grpcServer, n)
	}

	return n, nil
}

func (n *VNetServer) getResource(realm string, id string) (*pb.VNet, error) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := []byte(fmt.Sprintf("realm/%s/vnets/%s", realm, id))
	o := &pb.VNet{}

	err := n.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		err = item.Value(func(v []byte) error {
			if err := protojson.Unmarshal(v, o); err != nil {
				return err
			}
			return nil
		})

		return err
	})
	if err != nil {
		return nil, err
	}

	return o, nil
}

func (n *VNetServer) deleteResource(realm string, id string) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := []byte(fmt.Sprintf("realm/%s/vnets/%s", realm, id))

	err := n.db.DropPrefix([]byte(key))
	if err != nil {
		return err
	}

	n.publishUpdate(id, string(key), nil, false)

	return nil
}

func (n *VNetServer) listResources(realm string, key string) ([]*pb.VNet, error) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	prefix := []byte(fmt.Sprintf("realm/%s/vnets", realm))
	if key != "" {
		prefix = []byte(key)
	}
	list := []*pb.VNet{}

	n.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		//prefix := []byte(fmt.Sprintf("%s/", kind))
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			kk := item.Key()
			if len(prefix) < len(kk) {
				if bytes.IndexByte(kk[len(prefix)+1:], byte('/')) != -1 {
					continue
				}
			}

			err := item.Value(func(val []byte) error {
				o := &pb.VNet{}
				if err := protojson.Unmarshal(val, o); err != nil {
					return err
				}

				list = append(list, o)
				return nil
			})
			if err != nil {
				log.Errorf("Error processing element: %v", err)
				continue
			}
			if len(prefix) == len(kk) {
				break
			}
		}
		return nil
	})

	return list, nil
}

func (n *VNetServer) persistAndPublish(realm string, id string, data *pb.VNet) error {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	key := fmt.Sprintf("realm/%s/vnets/%s", realm, id)
	data.Key = key
	jsonData, err := protojson.Marshal(data)
	if err != nil {
		return err
	}

	err = n.db.Update(func(txn *badger.Txn) error {
		err := txn.Set([]byte(key), jsonData)
		return err
	})
	if err != nil {
		log.Errorf("Failed to persist object: %v", err)
		return err
	}

	n.publishUpdate(id, key, data, false)

	return nil
}

func (n *VNetServer) publishUpdate(id string, key string, data *pb.VNet, isSnapshot bool) error {
	action := pb.WatchVNetsResponse_SNAPSHOT

	if !isSnapshot {
		if data != nil {
			action = pb.WatchVNetsResponse_CHANGE
		} else {
			action = pb.WatchVNetsResponse_DELETE
		}
	}

	notification := &pb.WatchVNetsResponse{
		Action: action,
		Key:    key,
		Id:     id,
		Vnet:   data,
	}

	for clientName, sub := range n.clientChannels {
		//if strings.HasPrefix(key, sub.Kind) &&
		if sub.ID == "" || sub.ID == id {
			log.Infof("Sending update to: [%s] for key: [%s]", clientName, key)
			sub.Channel <- notification
		}
	}

	return nil
}

//Subscribe subscribes to route data
func (n *VNetServer) Subscribe(clientName string, realm string, id string, key string) (chan *pb.WatchVNetsResponse, error) {
	log.Infof("Subscribing client: [%s] on kind: [%s] and id: [%s]", clientName, "vnet", id)

	n.mtx.Lock()

	clientChan := make(chan *pb.WatchVNetsResponse, 100)
	n.clientChannels[clientName] = SubscriptionChannel{
		ID:      id,
		Channel: clientChan,
	}
	n.mtx.Unlock()

	// Send the current state on connection
	log.Infof("Sending initial update to: [%s]", clientName)

	list, err := n.listResources(realm, key)
	if err == nil {
		for _, data := range list {
			n.publishUpdate(id, data.Key, data, true)
		}
		// DARIO: TODO: cleanup on empty snap!
		l := len(list)
		if l != 0 {
			n.publishUpdate(id, list[l-1].Key, list[l-1], false)
		}
	}

	return clientChan, nil
}

//Unsubscribe unsubscribes from route data
func (n *VNetServer) Unsubscribe(clientName string) {
	n.mtx.Lock()
	defer n.mtx.Unlock()

	log.Infof("Unsubscribing client: [%s]", clientName)
	clientSub, ok := n.clientChannels[clientName]
	if ok {
		close(clientSub.Channel)
		delete(n.clientChannels, clientName)
	}
}

// ListVNets returns all the vnets defined.
func (n *VNetServer) ListVNets(context.Context, *pb.ListVNetsRequest) (*pb.VNets, error) {
	log.Debugf("VNetSErver:ListVNets called")

	vnets, err := n.listResources("default", "")
	if err != nil {
		log.Errorf("Failed to enumerate objects: %v", err)
		return nil, err
	}

	vv := &pb.VNets{Vnet: vnets}
	return vv, nil
}

// CreateVNet creates a new vnet.
func (n *VNetServer) CreateVNet(ctx context.Context, vnetReq *pb.CreateVNetRequest) (*pb.VNet, error) {
	log.Debugf("VNetSErver:CreateVNet called")
	realm := "default"

	// validate vnet params
	vnet, err := n.getResource(realm, vnetReq.Vnet.Id)
	if vnet != nil {
		return nil, fmt.Errorf("Vnet already exists")
	}

	sp := helpers.NewSimplePoolIP("111")
	sp.InitializeElements(vnetReq.Vnet.Subnet, vnetReq.ReservedIP)
	i, err := sp.GetItem("", "12344")
	i = i

	vnetReq.Vnet.IpPool = sp.GetInner()

	err = n.persistAndPublish(realm, vnetReq.Vnet.Id, vnetReq.Vnet)
	if err != nil {
		log.Errorf("Failed to persist object: %v", err)
		return nil, err
	}

	return vnetReq.Vnet, nil
}

func (n *VNetServer) GetVNet(ctx context.Context, req *pb.GetVNetRequest) (*pb.VNet, error) {
	realm := "default"
	vnet, err := n.getResource(realm, req.Id)
	if err == badger.ErrKeyNotFound {
		err = status.Error(codes.NotFound, err.Error())
	}

	return vnet, err
}

func (n *VNetServer) DeleteVNet(ctx context.Context, req *pb.DeleteVNetRequest) (*pb.Empty, error) {
	realm := "default"
	err := n.deleteResource(realm, req.Id)

	res := &pb.Empty{}

	return res, err
}

func (n *VNetServer) WatchVNets(req *pb.WatchVNetsRequest, stream pb.NetworkD_WatchVNetsServer) error {
	log.Debugf("VNetSErver:WatchVNets called")

	p, _ := peer.FromContext(stream.Context())
	peerId := p.Addr.String()

	realm := "default"
	discoveryData, err := n.Subscribe(peerId, realm, req.Id, "")
	if discoveryData == nil {
		log.WithError(err).Error("Failed with: %v", err)
		return err
	}

loop:
	for {
		select {
		case data := <-discoveryData:
			if err := stream.Send(data); err != nil {
				return err
			}

		case <-stream.Context().Done():
			break loop
		}
	}

	log.Infof("WatchVNets finishing [err: %v]", err)
	n.Unsubscribe(peerId)

	return nil
}

func (n *VNetServer) AllocateIp(ctx context.Context, req *pb.AllocateIpRequest) (*pb.AllocateIpResponse, error) {
	log.Debugf("VNetServer:AllocateIp called")

	realm := "default"
	vnet, err := n.getResource(realm, req.VnetID)
	if err == badger.ErrKeyNotFound {
		err = status.Error(codes.NotFound, err.Error())
		return nil, err
	}

	ip := req.IpAddress
	var sp *helpers.SimplePoolEx
	if !req.PassThrough {
		sp := helpers.GetSimplePool(vnet.IpPool)
		ipItem, err := sp.GetItem(req.IpAddress, req.ContainerID)
		if err != nil {
			err = status.Error(codes.ResourceExhausted, err.Error())
			return nil, err
		}

		ip = ipItem.Value
	}

	nicConfig := req.NicConfiguration
	nicConfig.IPConfiguration = &pb.IPConfiguration{
		IPAddress:   ip,
		GWAddress:   vnet.GwIPAddress,
		DnsConfig:   vnet.DnsConfig,
		ContainerID: req.ContainerID,
		DnsName:     req.DnsName,
	}

	if !req.PassThrough {
		err = n.persistAndPublish(realm, vnet.Id, vnet)
		if err != nil {
			log.Errorf("Failed to persist object: %v", err)
			return nil, err
		}
	}

	err = n.persistAndPublishNic(realm, req.VnetID, req.ContainerID, nicConfig)
	if err != nil {
		if !req.PassThrough {
			sp.ReleaseItem(req.ContainerID)
		}
		err = status.Error(codes.Internal, err.Error())
		return nil, err
	}

	res := &pb.AllocateIpResponse{
		ContainerID:      req.ContainerID,
		NicConfiguration: nicConfig,
		Vnet:             vnet,
	}

	return res, nil
}

func (n *VNetServer) DeallocateIp(ctx context.Context, req *pb.DeallocateIpRequest) (*pb.DeallocateIpResponse, error) {
	log.Debugf("VNetServer:DeallocateIp called")

	realm := "default"
	vnet, err := n.getResource(realm, req.VnetID)
	if err == badger.ErrKeyNotFound {
		err = status.Error(codes.NotFound, err.Error())
		return nil, err
	}

	sp := helpers.GetSimplePool(vnet.IpPool)
	ip, err := sp.ReleaseItem(req.ContainerID)
	if err != nil {
		err = status.Error(codes.ResourceExhausted, err.Error())
		return nil, err
	}

	err = n.persistAndPublish(realm, vnet.Id, vnet)
	if err != nil {
		log.Errorf("Failed to persist object: %v", err)
		return nil, err
	}

	err = n.deleteResourceNic(realm, req.VnetID, req.ContainerID)
	if err != nil {
		err = status.Error(codes.Internal, err.Error())
		return nil, err
	}

	res := &pb.DeallocateIpResponse{
		VnetID:      vnet.Id,
		ContainerID: req.ContainerID,
		IpAddress:   ip,
	}

	return res, nil
}
