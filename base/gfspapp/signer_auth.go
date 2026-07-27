package gfspapp

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const gfSpSignFullMethod = "/base.types.gfspserver.GfSpSignService/GfSpSign"

func signerAuthUnaryInterceptor(allowedClientURIs []string) grpc.UnaryServerInterceptor {
	allowed := make(map[string]struct{}, len(allowedClientURIs))
	for _, uri := range allowedClientURIs {
		allowed[uri] = struct{}{}
	}

	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if info.FullMethod != gfSpSignFullMethod {
			return handler(ctx, req)
		}

		clientURIs, ok := verifiedClientURIs(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "verified client certificate required")
		}
		for _, uri := range clientURIs {
			if _, ok = allowed[uri]; ok {
				return handler(ctx, req)
			}
		}
		return nil, status.Error(codes.PermissionDenied, "client certificate identity is not authorized for signer")
	}
}

func verifiedClientURIs(ctx context.Context) ([]string, bool) {
	clientPeer, ok := peer.FromContext(ctx)
	if !ok {
		return nil, false
	}
	tlsInfo, ok := clientPeer.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return nil, false
	}

	leaf := tlsInfo.State.VerifiedChains[0][0]
	clientURIs := make([]string, 0, len(leaf.URIs))
	for _, uri := range leaf.URIs {
		clientURIs = append(clientURIs, uri.String())
	}
	return clientURIs, true
}
