package helpers

import (
	"context"
	"crypto/tls"
	"crypto/x509"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type tokenAuth struct {
	token string
}

// Return value is mapped to request headers.
func (t tokenAuth) GetRequestMetadata(ctx context.Context, in ...string) (map[string]string, error) {
	return map[string]string{
		//"authorization": "Bearer " + t.token,
		"authorization": t.token,
	}, nil
}

func (tokenAuth) RequireTransportSecurity() bool {
	return false
}

func GetGrpcClient(pubsubEndpoint string, token string) (*grpc.ClientConn, error) {
	roots := x509.NewCertPool()

	tlsconfig := &tls.Config{
		RootCAs:            roots,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			return nil
		},
	}

	transportCreds := credentials.NewTLS(tlsconfig)

	connCtx, _ := context.WithCancel(context.Background())
	conn, err := grpc.DialContext(connCtx, pubsubEndpoint,
		//grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithTransportCredentials(transportCreds),
		grpc.WithPerRPCCredentials(tokenAuth{
			token: token,
		}))

	//conn, err := grpc.Dial(pubsubEndpoint, grpc.WithInsecure())
	if err != nil {
		log.Fatal(err)
	}

	return conn, err
}
