package helpers

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/dgrijalva/jwt-go"
	log "github.com/sirupsen/logrus"
)

type jwtCustomClaims struct {
	jwt.StandardClaims
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

type AuthInterceptor struct {
	key string
}

func NewAuthInterceptor(key string) *AuthInterceptor {
	return &AuthInterceptor{
		key: key,
	}
}

func (in *AuthInterceptor) parseToken(token string) (*jwtCustomClaims, error) {
	t, err := jwt.ParseWithClaims(
		token,
		&jwtCustomClaims{},
		func(t *jwt.Token) (interface{}, error) {
			_, ok := t.Method.(*jwt.SigningMethodHMAC)
			if !ok {
				return nil, fmt.Errorf("unexpected token signing method")
			}

			return []byte(in.key), nil
		},
	)

	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := t.Claims.(*jwtCustomClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

func (in *AuthInterceptor) authorize(ctx context.Context, method string) error {
	//return nil
	if strings.Contains(method, "ServerReflectionInfo") {
		return nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Errorf(codes.Unauthenticated, "no metadata")
	}

	values := md["authorization"]
	if len(values) == 0 {
		return status.Errorf(codes.Unauthenticated, "no auth token")
	}

	claims, err := in.parseToken(values[0])
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "access token is invalid: %v", err)
	}

	if claims.Admin {
		return nil
	}

	return status.Error(codes.PermissionDenied, "no access")
}

func (in *AuthInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		log.Println("--> unary interceptor: ", info.FullMethod)

		err := in.authorize(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

func (in *AuthInterceptor) Stream() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		stream grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		log.Println("--> stream interceptor: ", info.FullMethod)

		err := in.authorize(stream.Context(), info.FullMethod)
		if err != nil {
			return err
		}

		return handler(srv, stream)
	}
}
