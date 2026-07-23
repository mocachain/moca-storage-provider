package p2p

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/core/module"
	"github.com/mocachain/moca-storage-provider/core/rcmgr"
)

func TestP2PModularName(t *testing.T) {
	require.Equal(t, module.P2PModularName, (&P2PModular{}).Name())
}

func TestP2PModularReserveResource(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	state := &rcmgr.ScopeStat{Memory: 64}

	t.Run("reserves resources in a new span", func(t *testing.T) {
		scope := rcmgr.NewMockResourceScope(ctrl)
		span := rcmgr.NewMockResourceScopeSpan(ctrl)
		scope.EXPECT().BeginSpan().Return(span, nil)
		span.EXPECT().ReserveResources(state).Return(nil)

		reservedSpan, err := (&P2PModular{scope: scope}).ReserveResource(context.Background(), state)
		require.NoError(t, err)
		require.Same(t, span, reservedSpan)
	})

	t.Run("returns the span creation error", func(t *testing.T) {
		scope := rcmgr.NewMockResourceScope(ctrl)
		scope.EXPECT().BeginSpan().Return(nil, errors.New("scope unavailable"))

		reservedSpan, err := (&P2PModular{scope: scope}).ReserveResource(context.Background(), state)
		require.Error(t, err)
		require.Nil(t, reservedSpan)
	})

	t.Run("returns the resource reservation error", func(t *testing.T) {
		scope := rcmgr.NewMockResourceScope(ctrl)
		span := rcmgr.NewMockResourceScopeSpan(ctrl)
		scope.EXPECT().BeginSpan().Return(span, nil)
		span.EXPECT().ReserveResources(state).Return(errors.New("limit reached"))

		reservedSpan, err := (&P2PModular{scope: scope}).ReserveResource(context.Background(), state)
		require.Error(t, err)
		require.Nil(t, reservedSpan)
	})
}

func TestP2PModularReleaseResourceEndsSpan(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	span := rcmgr.NewMockResourceScopeSpan(ctrl)
	span.EXPECT().Done()

	(&P2PModular{}).ReleaseResource(context.Background(), span)
}
