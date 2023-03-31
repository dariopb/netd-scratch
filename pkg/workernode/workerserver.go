package workernode

import (
	"context"
	"fmt"
	"strconv"

	//"google.golang.org/grpc/peer"

	//"google.golang.org/protobuf/encoding/protojson"

	//"github.com/dariopb/netd/pkg/helpers"

	pb "github.com/dariopb/netd/proto/netd.v1alpha"
	pbNetNode "github.com/dariopb/netd/proto/netnode.v1alpha"

	log "github.com/sirupsen/logrus"
)

type WorkerServer struct {
	pbNetNode.UnimplementedNetNodeServer

	wn *WorkerNode
	//vnets map[string]*pb.VNet

	//mtx sync.Mutex
}

func NewWorkerServer(wn *WorkerNode) (*WorkerServer, error) {
	n := &WorkerServer{
		wn: wn,
	}

	return n, nil
}

func (ws *WorkerServer) AllocateIp(ctx context.Context, req *pbNetNode.AllocateIpRequest) (*pbNetNode.AllocateIpResponse, error) {
	log.Debugf("VNetServer:AllocateIp called")

	nicID := "nic-" + getHash(req.ContainerID)
	publicKey, _, err := getOrGenerateKeys(nicID)
	if err != nil {
		return nil, err
	}

	if req.NicType == pb.NicType_NOTHING {
		req.NicType = pb.NicType_TAP
	}

	// get a port for the tunnel
	vlanId, err := ws.wn.getVlanIDAndPersist(nicID)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(vlanId)
	if err != nil {
		return nil, err
	}
	port += ws.wn.basePort

	nicConfig := &pb.NicConfiguration{
		Name:       nicID,
		Vnet:       req.VnetID,
		IsGW:       req.IsDefaultGW,
		PublicKey:  publicKey,
		Endpoint:   fmt.Sprintf("%s:%d", ws.wn.outboundIP, port),
		ListenPort: strconv.Itoa(port),
		NicType:    req.NicType,
		Labels:     req.Labels,
	}

	if req.DnsName == "" {
		req.DnsName = req.ContainerID
	}

	log.Infof("VNetServer:AllocateIp: requesting nic info for containerId: [%s], nicId: [%s], dnsName: [%s], publicKey: [%s]",
		req.ContainerID, nicID, req.DnsName, publicKey)

	r := pb.AllocateIpRequest{
		ContainerID:      nicID, //req.ContainerID,
		VnetID:           req.VnetID,
		IpAddress:        req.IpAddress,
		DnsName:          req.DnsName,
		NicConfiguration: nicConfig,
	}
	ctx1, _ := context.WithTimeout(ctx, ws.wn.grpcTimeout)
	allocRes, err := ws.wn.vnetClient.AllocateIp(ctx1, &r)
	if err != nil {
		ws.wn.releaseVlanIDAndPersist(vlanId)
		return nil, err
	}

	ipConfig := allocRes.NicConfiguration.IPConfiguration
	vnetConfig := allocRes.Vnet

	res := &pbNetNode.AllocateIpResponse{
		ContainerID: allocRes.ContainerID,
		//Vnet:        allocRes.Vnet,
		IpConfiguration: &pbNetNode.IPConfigurationLocal{
			IPAddress: ipConfig.IPAddress,
			GWAddress: ipConfig.GWAddress,
			DnsConfig: &pbNetNode.DNSConfig{
				Server: ipConfig.DnsConfig.Server,
				Search: ipConfig.DnsConfig.Search,
				Ndots:  ipConfig.DnsConfig.Ndots,
			},
			ContainerID:    ipConfig.ContainerID,
			DnsName:        ipConfig.DnsName,
			LocalInterface: nicID + "_t",
			NetNamespace:   "/var/run/netns/_alc_" + nicID,
		},
		Vnet: &pbNetNode.VNet{
			Id:          vnetConfig.Id,
			Subnet:      vnetConfig.Subnet,
			GwIPAddress: vnetConfig.GwIPAddress,
		},
	}

	log.Infof("VNetServer:AllocateIp: got nic info for nicId: [%s], publicKey: [%s]: %v", nicID, publicKey, res)

	// plumb the local endpoint
	err = ws.wn.setupLocalEndpoint(allocRes.Vnet, allocRes.NicConfiguration, vlanId)
	if err != nil {
		// clean up
		ws.wn.releaseVlanIDAndPersist(vlanId)
		return nil, err
	}

	return res, nil
}

func (ws *WorkerServer) DeallocateIp(ctx context.Context, req *pbNetNode.DeallocateIpRequest) (*pbNetNode.DeallocateIpResponse, error) {
	log.Debugf("VNetServer:DeallocateIp called")

	nicID := "nic-" + getHash(req.ContainerID)

	r := pb.DeallocateIpRequest{
		ContainerID: nicID, //req.ContainerID,
		VnetID:      req.VnetID,
	}
	ctx1, _ := context.WithTimeout(ctx, ws.wn.grpcTimeout)
	deallocRes, err := ws.wn.vnetClient.DeallocateIp(ctx1, &r)
	if err != nil {
		return nil, err
	}

	res := &pbNetNode.DeallocateIpResponse{
		ContainerID: deallocRes.ContainerID,
		IpConfiguration: &pbNetNode.IPConfigurationLocal{
			ContainerID: deallocRes.ContainerID,
			IPAddress:   deallocRes.IpAddress,
		},
	}

	// remove the local endpoint
	err = ws.wn.removeLocalEndpoint(req.VnetID, nicID)
	if err != nil {
		return nil, err
	}

	return res, nil
}
