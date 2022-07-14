// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.2.0
// - protoc             v3.20.0
// source: netnode.proto

package netnode_v1alpha

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// NetNodeClient is the client API for NetNode service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type NetNodeClient interface {
	// AllocateIp is called by the local runtime to allocate an IP Address and register a "nic" with the network.
	AllocateIp(ctx context.Context, in *AllocateIpRequest, opts ...grpc.CallOption) (*AllocateIpResponse, error)
	// DeallocateIpRequest is called by the local runtime to release an IP address.
	DeallocateIp(ctx context.Context, in *DeallocateIpRequest, opts ...grpc.CallOption) (*DeallocateIpResponse, error)
}

type netNodeClient struct {
	cc grpc.ClientConnInterface
}

func NewNetNodeClient(cc grpc.ClientConnInterface) NetNodeClient {
	return &netNodeClient{cc}
}

func (c *netNodeClient) AllocateIp(ctx context.Context, in *AllocateIpRequest, opts ...grpc.CallOption) (*AllocateIpResponse, error) {
	out := new(AllocateIpResponse)
	err := c.cc.Invoke(ctx, "/netnode.v1alpha.NetNode/AllocateIp", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *netNodeClient) DeallocateIp(ctx context.Context, in *DeallocateIpRequest, opts ...grpc.CallOption) (*DeallocateIpResponse, error) {
	out := new(DeallocateIpResponse)
	err := c.cc.Invoke(ctx, "/netnode.v1alpha.NetNode/DeallocateIp", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// NetNodeServer is the server API for NetNode service.
// All implementations must embed UnimplementedNetNodeServer
// for forward compatibility
type NetNodeServer interface {
	// AllocateIp is called by the local runtime to allocate an IP Address and register a "nic" with the network.
	AllocateIp(context.Context, *AllocateIpRequest) (*AllocateIpResponse, error)
	// DeallocateIpRequest is called by the local runtime to release an IP address.
	DeallocateIp(context.Context, *DeallocateIpRequest) (*DeallocateIpResponse, error)
	mustEmbedUnimplementedNetNodeServer()
}

// UnimplementedNetNodeServer must be embedded to have forward compatible implementations.
type UnimplementedNetNodeServer struct {
}

func (UnimplementedNetNodeServer) AllocateIp(context.Context, *AllocateIpRequest) (*AllocateIpResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method AllocateIp not implemented")
}
func (UnimplementedNetNodeServer) DeallocateIp(context.Context, *DeallocateIpRequest) (*DeallocateIpResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DeallocateIp not implemented")
}
func (UnimplementedNetNodeServer) mustEmbedUnimplementedNetNodeServer() {}

// UnsafeNetNodeServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to NetNodeServer will
// result in compilation errors.
type UnsafeNetNodeServer interface {
	mustEmbedUnimplementedNetNodeServer()
}

func RegisterNetNodeServer(s grpc.ServiceRegistrar, srv NetNodeServer) {
	s.RegisterService(&NetNode_ServiceDesc, srv)
}

func _NetNode_AllocateIp_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(AllocateIpRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(NetNodeServer).AllocateIp(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/netnode.v1alpha.NetNode/AllocateIp",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(NetNodeServer).AllocateIp(ctx, req.(*AllocateIpRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _NetNode_DeallocateIp_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(DeallocateIpRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(NetNodeServer).DeallocateIp(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/netnode.v1alpha.NetNode/DeallocateIp",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(NetNodeServer).DeallocateIp(ctx, req.(*DeallocateIpRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// NetNode_ServiceDesc is the grpc.ServiceDesc for NetNode service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var NetNode_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "netnode.v1alpha.NetNode",
	HandlerType: (*NetNodeServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "AllocateIp",
			Handler:    _NetNode_AllocateIp_Handler,
		},
		{
			MethodName: "DeallocateIp",
			Handler:    _NetNode_DeallocateIp_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "netnode.proto",
}
