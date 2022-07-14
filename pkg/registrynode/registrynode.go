package registrynode

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"google.golang.org/grpc"

	"github.com/dariopb/netd/pkg/certHelper"
	"github.com/dariopb/netd/pkg/helpers"
	service "github.com/dariopb/netd/pkg/service"
	servicecontroller "github.com/dariopb/netd/pkg/servicecontroller"
	vnet "github.com/dariopb/netd/pkg/vnet"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	log "github.com/sirupsen/logrus"
)

type RegistryNode struct {
	accessToken string
	conn        *grpc.ClientConn

	ctx      context.Context
	cancelFn context.CancelFunc
}

func getTLSCredentials() (credentials.TransportCredentials, error) {

	cert, err := certHelper.CreateDynamicTlsCertWithKey("localhost")
	if err != nil {
		return nil, err
	}

	// Create the credentials and return it
	config := &tls.Config{
		Certificates: []tls.Certificate{*cert},
		ClientAuth:   tls.NoClientCert,
	}

	return credentials.NewTLS(config), nil

}

// NewRegistryNode creates a worker node
func NewRegistryNode(dataDir string, hmacKey string, port int, defaultSubnet string) (*RegistryNode, error) {
	var err error

	log.Infof("Creating registryNode: dataDir: %s, TLS grpc port: %d, defaultSubnet: %s", dataDir, port, defaultSubnet)

	rn := &RegistryNode{
		accessToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJOYW1lIjoiMSIsIkFkbWluIjp0cnVlfQ.BMwxDsoP6sb5ijNTUsPGskZ6Rc_NP-_xbeQRM18d2-A",
	}

	tlsCreds, err := getTLSCredentials()
	if err != nil {
		log.Fatal("cannot get TLS credentials: ", err)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	interceptor := helpers.NewAuthInterceptor(hmacKey)

	opts := []grpc.ServerOption{
		grpc.Creds(tlsCreds),
		grpc.UnaryInterceptor(interceptor.Unary()),
		grpc.StreamInterceptor(interceptor.Stream()),
	}

	grpcServer := grpc.NewServer(opts...)

	vnetserver, err := vnet.NewVNetServer(dataDir, grpcServer)
	if err != nil {
		log.Fatalf("Failed to create VNetSErver impl: %v", err)
	}
	vnetserver = vnetserver

	// add the Service server
	serviceserver, err := service.NewServiceServer(dataDir+"-services", grpcServer)
	if err != nil {
		log.Fatalf("Failed to create ServiceServer impl: %v", err)
	}
	serviceserver = serviceserver

	reflection.Register(grpcServer)

	go func() {
		grpcServer.Serve(lis)
		if err != nil {
			log.Fatal(err)
		}
	}()

	rn.conn, err = helpers.GetGrpcClient(fmt.Sprintf("localhost:%d", port), rn.accessToken)
	if err != nil {
		return nil, err
	}

	rn.ctx, rn.cancelFn = context.WithCancel(context.TODO())

	err = rn.registerControllers()
	if err != nil {
		return nil, err
	}

	return rn, nil
}

func (rn *RegistryNode) registerControllers() error {

	sc, err := servicecontroller.NewServiceController(rn.conn, rn.ctx)
	if err != nil {
		return err
	}
	go sc.WatchServiceLoop(rn.ctx)

	return nil
}
