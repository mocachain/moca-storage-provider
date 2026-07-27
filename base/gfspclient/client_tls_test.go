package gfspclient

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestDefaultClientRejectsPlaintextServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client := mockBufClient()
	conn, err := client.Connection(ctx, mockBufNet, grpc.WithContextDialer(bufDialer))
	require.NoError(t, err)
	defer conn.Close()

	err = conn.Invoke(ctx, "/test.Transport/Probe", &emptypb.Empty{}, &emptypb.Empty{})
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err), "plaintext RPC reached the server")
}
