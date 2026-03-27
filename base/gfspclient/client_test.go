package gfspclient

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

const mockAddress = "localhost:0"

func TestErrRPCUnknownWithDetail(t *testing.T) {
	err := ErrRPCUnknownWithDetail("mock", errors.New("mock"))
	assert.NotNil(t, err)
}

func TestGfSpClient_Connection(t *testing.T) {
	s := mockBufClient()
	conn, err := s.Connection(context.TODO(), mockAddress)
	assert.Nil(t, err)
	defer conn.Close()
}

func TestGfSpClient_ManagerConnSuccess(t *testing.T) {
	s := mockBufClient()
	conn, err := s.ManagerConn(context.TODO())
	assert.Nil(t, err)
	defer conn.Close()
	assert.NotNil(t, conn)
}

func TestGfSpClient_ManagerConnCancelled(t *testing.T) {
	s := mockBufClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// gRPC dial is lazy; cancelled context may not fail until first RPC
	assert.NotPanics(t, func() {
		_, _ = s.ManagerConn(ctx)
	})
}

func TestGfSpClient_ApproverConnSuccess(t *testing.T) {
	s := mockBufClient()
	conn, err := s.ApproverConn(context.TODO())
	assert.Nil(t, err)
	defer conn.Close()
	assert.NotNil(t, conn)
}

func TestGfSpClient_ApproverConnCancelled(t *testing.T) {
	s := mockBufClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NotPanics(t, func() {
		_, _ = s.ApproverConn(ctx)
	})
}

func TestGfSpClient_P2PConnSuccess(t *testing.T) {
	s := mockBufClient()
	conn, err := s.P2PConn(context.TODO())
	assert.Nil(t, err)
	defer conn.Close()
	assert.NotNil(t, conn)
}

func TestGfSpClient_P2PConnCancelled(t *testing.T) {
	s := mockBufClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NotPanics(t, func() {
		_, _ = s.P2PConn(ctx)
	})
}

func TestGfSpClient_SignerConnSuccess(t *testing.T) {
	s := mockBufClient()
	conn, err := s.SignerConn(context.TODO())
	assert.Nil(t, err)
	defer conn.Close()
	assert.NotNil(t, conn)
}

func TestGfSpClient_SignerConnCancelled(t *testing.T) {
	s := mockBufClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NotPanics(t, func() {
		_, _ = s.SignerConn(ctx)
	})
}

func TestGfSpClient_HTTPClient(t *testing.T) {
	s := mockBufClient()
	result := s.HTTPClient(context.TODO())
	assert.NotNil(t, result)
}

func TestGfSpClient_Close(t *testing.T) {
	s := mockBufClient()
	conn1, err1 := s.ManagerConn(context.TODO())
	assert.Nil(t, err1)
	s.managerConn = conn1
	conn2, err2 := s.ApproverConn(context.TODO())
	assert.Nil(t, err2)
	s.approverConn = conn2
	conn3, err3 := s.P2PConn(context.TODO())
	assert.Nil(t, err3)
	s.p2pConn = conn3
	conn4, err4 := s.SignerConn(context.TODO())
	assert.Nil(t, err4)
	s.signerConn = conn4
	err := s.Close()
	assert.Nil(t, err)
}
