package util

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/peer"
)

type addr struct {
	ipAddress string
}

func (addr) Network() string   { return "" }
func (a *addr) String() string { return a.ipAddress }

func TestGetRPCRemoteAddress(t *testing.T) {
	cases := []struct {
		name         string
		ctx          context.Context
		wantedResult string
	}{
		{
			"Context without peer",
			peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}}),
			"127.0.0.1",
		},
		{
			"Context with correct IP address",
			peer.NewContext(context.Background(), &peer.Peer{Addr: &addr{ipAddress: "127.0.0.1:9000"}}),
			"127.0.0.1:9000",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRPCRemoteAddress(tt.ctx)
			assert.Equal(t, tt.wantedResult, result)
		})
	}
}
