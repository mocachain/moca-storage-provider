package gfspapp

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mocachain/moca-storage-provider/util"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/mocachain/moca-storage-provider/base/types/gfspserver"
)

func TestGfSpBaseApp_GfSpSignRejectsUnauthenticatedRequest(t *testing.T) {
	g := &GfSpBaseApp{}
	g.newRPCServer()

	lis := bufconn.Listen(1024 * 1024)
	go func() {
		_ = g.server.Serve(lis)
	}()
	t.Cleanup(func() {
		g.server.Stop()
		_ = lis.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	assert.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = gfspserver.NewGfSpSignServiceClient(conn).GfSpSign(ctx, &gfspserver.GfSpSignRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

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
