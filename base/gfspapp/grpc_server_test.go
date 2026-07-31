package gfspapp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/mocachain/moca-storage-provider/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestSignerAuthUnaryInterceptor(t *testing.T) {
	const (
		signerMethod = "/base.types.gfspserver.GfSpSignService/GfSpSign"
		allowedURI   = "spiffe://mocachain/sp/manager"
	)
	interceptor := signerAuthUnaryInterceptor([]string{allowedURI})
	info := &grpc.UnaryServerInfo{FullMethod: signerMethod}
	handlerCalled := false
	handler := func(context.Context, interface{}) (interface{}, error) {
		handlerCalled = true
		return "ok", nil
	}

	t.Run("missing client certificate", func(t *testing.T) {
		handlerCalled = false
		_, err := interceptor(context.Background(), nil, info, handler)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
		assert.False(t, handlerCalled)
	})

	t.Run("verified but unauthorized URI SAN", func(t *testing.T) {
		handlerCalled = false
		ctx := verifiedClientContext(t, "spiffe://mocachain/sp/untrusted")
		_, err := interceptor(ctx, nil, info, handler)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
		assert.False(t, handlerCalled)
	})

	t.Run("allowlisted URI SAN", func(t *testing.T) {
		handlerCalled = false
		ctx := verifiedClientContext(t, allowedURI)
		response, err := interceptor(ctx, nil, info, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", response)
		assert.True(t, handlerCalled)
	})

	t.Run("other service method", func(t *testing.T) {
		handlerCalled = false
		response, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/other.Service/Call"}, handler)
		require.NoError(t, err)
		assert.Equal(t, "ok", response)
		assert.True(t, handlerCalled)
	})
}

func verifiedClientContext(t *testing.T, uri string) context.Context {
	t.Helper()
	parsedURI, err := url.Parse(uri)
	require.NoError(t, err)
	certificate := &x509.Certificate{URIs: []*url.URL{parsedURI}}
	tlsInfo := credentials.TLSInfo{State: tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{certificate},
		VerifiedChains:   [][]*x509.Certificate{{certificate}},
	}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: tlsInfo})
}

func TestGfSpBaseApp_ReflectionDisabledByDefault(t *testing.T) {
	g := &GfSpBaseApp{}
	g.newRPCServer(insecure.NewCredentials(), false)

	services := g.server.GetServiceInfo()
	_, registered := services[reflectionServiceName]
	assert.False(t, registered, "grpc reflection must not be registered unless explicitly enabled")
	// the gate must only cover reflection, the sp services stay registered
	assert.Contains(t, services, signServiceName)
}

func TestGfSpBaseApp_ReflectionEnabledByConfig(t *testing.T) {
	g := &GfSpBaseApp{}
	g.newRPCServer(insecure.NewCredentials(), true)

	services := g.server.GetServiceInfo()
	_, registered := services[reflectionServiceName]
	assert.True(t, registered)
	assert.Contains(t, services, signServiceName)
}

// signServiceName is one of the sp services, used to assert the reflection gate
// does not skip the service registration.
const signServiceName = "base.types.gfspserver.GfSpSignService"

func TestGfSpBaseApp_StartRPCServerSuccess(t *testing.T) {
	g := &GfSpBaseApp{grpcAddress: "localhost:0"}
	g.server = grpc.NewServer()
	go func() {
		// make sure Serve() is called
		time.Sleep(time.Millisecond * 500)
		err := g.StopRPCServer(context.TODO())
		assert.Nil(t, err)
	}()
	err := g.StartRPCServer(context.TODO())
	assert.Nil(t, err)
}

type addr struct {
	ipAddress string
}

func (addr) Network() string   { return "" }
func (a *addr) String() string { return a.ipAddress }

func TestGetIPFromGRPCContext(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		addr string
	}{
		{
			"Context without addr",
			context.Background(),
			"",
		},
		{
			"Context with correct IP address",
			peer.NewContext(context.Background(), &peer.Peer{Addr: &addr{ipAddress: "127.0.0.1:9000"}}),
			"127.0.0.1:9000",
		},
		{
			"Context with correct IP address",
			peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}}),
			"127.0.0.1",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			addr := util.GetRPCRemoteAddress(tt.ctx)
			assert.Equal(t, tt.addr, addr)
		})
	}
}
