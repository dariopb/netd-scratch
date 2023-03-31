package types

import (
	pb "github.com/dariopb/netd/proto/netd.v1alpha"
)

type VNETDataWorker struct {
}

type NicData struct {
}

type LocalNicData struct {
	NicId      string
	NS         string
	LocalOutIP string
	IP         string
}

type NetworkNodeOps interface {
	// Implement the vrf for a network
	SetupVRF(n *pb.VNet, nic *pb.NicConfiguration, localIndex string) (*LocalNicData, error)
	RemoveVRF(ns string) error

	CleanupVRF(vnetID string, nicID string, localIndex string) error
	CleanupAllVRFs() error

	AddLocalEndpoint(n *pb.VNet, nic *pb.NicConfiguration) error
	RemoveLocalEndpoint(n *pb.VNet, nic *pb.NicConfiguration) error

	AddEndpoint(vnetId string, nicId string, nic *pb.NicConfiguration) error
	RemoveEndpoint(vnetID string, nicId string, nic *pb.NicConfiguration) error
}
