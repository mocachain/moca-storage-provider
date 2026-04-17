package util

import (
	"context"
	"errors"
	"math"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/0xPolygon/polygon-edge/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	storagetypes "github.com/evmos/evmos/v12/x/storage/types"
	virtualgrouptypes "github.com/evmos/evmos/v12/x/virtualgroup/types"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/core/consensus"
)

func mustMarshalValidBLSSignatures(t *testing.T, total int) [][]byte {
	t.Helper()

	signatures := make([][]byte, 0, total)
	for i := 0; i < total; i++ {
		key, err := bls.GenerateBlsKey()
		require.NoError(t, err)

		signature, err := key.Sign([]byte("aggregate-test"), []byte("test-domain"))
		require.NoError(t, err)

		raw, err := signature.Marshal()
		require.NoError(t, err)
		signatures = append(signatures, raw)
	}

	return signatures
}

func TestGetSecondarySPIndexFromGVG(t *testing.T) {
	cases := []struct {
		name         string
		gvg          *virtualgrouptypes.GlobalVirtualGroup
		spID         uint32
		wantedResult int32
		wantedErr    error
	}{
		{
			name: "In secondary SPs",
			gvg: &virtualgrouptypes.GlobalVirtualGroup{
				SecondarySpIds: []uint32{1, 2, 3},
			},
			spID:         1,
			wantedResult: 0,
			wantedErr:    nil,
		},
		{
			name: "Not in secondary SPs",
			gvg: &virtualgrouptypes.GlobalVirtualGroup{
				SecondarySpIds: []uint32{1, 2, 3},
			},
			spID:         4,
			wantedResult: -1,
			wantedErr:    ErrNotInSecondarySPs,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetSecondarySPIndexFromGVG(tt.gvg, tt.spID)
			assert.Equal(t, tt.wantedResult, result)
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func TestValidateAndGetSPIndexWithinGVGSecondarySPsSuccess1(t *testing.T) {
	t.Log("Success case description: current sp is one of the object gvg's secondary sp")
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, bucketID uint64, lvgID uint32, opts ...grpc.DialOption) (
			*virtualgrouptypes.GlobalVirtualGroup, error,
		) {
			return &virtualgrouptypes.GlobalVirtualGroup{SecondarySpIds: []uint32{1, 2, 3, 4, 5}}, nil
		}).AnyTimes()
	result1, result2, err := ValidateAndGetSPIndexWithinGVGSecondarySPs(context.Background(), m, 3, 1, 2)
	assert.Equal(t, 2, result1)
	assert.Equal(t, true, result2)
	assert.Nil(t, err)
}

func TestValidateAndGetSPIndexWithinGVGSecondarySPsSuccess2(t *testing.T) {
	t.Log("Success case description: current sp is not one of the object gvg's secondary sp")
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, bucketID uint64, lvgID uint32, opts ...grpc.DialOption) (
			*virtualgrouptypes.GlobalVirtualGroup, error,
		) {
			return &virtualgrouptypes.GlobalVirtualGroup{SecondarySpIds: []uint32{1, 2, 3, 4, 5}}, nil
		}).AnyTimes()
	result1, result2, err := ValidateAndGetSPIndexWithinGVGSecondarySPs(context.Background(), m, 8, 1, 2)
	assert.Equal(t, -1, result1)
	assert.Equal(t, false, result2)
	assert.Nil(t, err)
}

func TestValidateAndGetSPIndexWithinGVGSecondarySPsFailure(t *testing.T) {
	t.Log("Failure case description: call GetGlobalVirtualGroup returns error")
	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, bucketID uint64, lvgID uint32, opts ...grpc.DialOption) (
			*virtualgrouptypes.GlobalVirtualGroup, error,
		) {
			return nil, errors.New("mock error")
		}).AnyTimes()
	result1, result2, err := ValidateAndGetSPIndexWithinGVGSecondarySPs(context.Background(), m, 8, 1, 2)
	assert.Equal(t, -1, result1)
	assert.Equal(t, false, result2)
	assert.Equal(t, errors.New("mock error"), err)
}

func TestTotalStakingStoreSizeOfGVG(t *testing.T) {
	cases := []struct {
		name            string
		gvg             *virtualgrouptypes.GlobalVirtualGroup
		stakingPerBytes sdkmath.Int
		wantedResult    uint64
	}{
		{
			name: "Return right result",
			gvg: &virtualgrouptypes.GlobalVirtualGroup{
				TotalDeposit: sdkmath.NewInt(100),
			},
			stakingPerBytes: sdkmath.NewInt(10),
			wantedResult:    10,
		},
		{
			name: "Return math.MaxUint64",
			gvg: &virtualgrouptypes.GlobalVirtualGroup{
				TotalDeposit: sdkmath.NewInt(-1),
			},
			stakingPerBytes: sdkmath.NewInt(1),
			wantedResult:    math.MaxUint64,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result := TotalStakingStoreSizeOfGVG(tt.gvg, tt.stakingPerBytes)
			assert.Equal(t, tt.wantedResult, result)
		})
	}
}

func TestValidateSecondarySPs(t *testing.T) {
	cases := []struct {
		name           string
		selfSpID       uint32
		secondarySpIDs []uint32
		wantedResult1  int
		wantedResult2  bool
	}{
		{
			name:           "Is secondary sp",
			selfSpID:       1,
			secondarySpIDs: []uint32{1, 2, 3},
			wantedResult1:  0,
			wantedResult2:  true,
		},
		{
			name:           "Not secondary sp",
			selfSpID:       4,
			secondarySpIDs: []uint32{1, 2, 3},
			wantedResult1:  -1,
			wantedResult2:  false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result1, result2 := ValidateSecondarySPs(tt.selfSpID, tt.secondarySpIDs)
			assert.Equal(t, tt.wantedResult1, result1)
			assert.Equal(t, tt.wantedResult2, result2)
		})
	}
}

func TestValidatePrimarySP(t *testing.T) {
	result := ValidatePrimarySP(1, 1)
	assert.Equal(t, result, true)
}

func TestBlsAggregate(t *testing.T) {
	cases := []struct {
		name          string
		secondarySigs [][]byte
		wantedResult  []byte
		wantedIsErr   bool
	}{
		{
			name: "Aggregate bls signature correctly",
			secondarySigs: mustMarshalValidBLSSignatures(t, 4),
			wantedIsErr: false,
		},
		{
			name:          "Cannot aggregate bls signature",
			secondarySigs: [][]byte{[]byte{1}},
			wantedIsErr:   true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := BlsAggregate(tt.secondarySigs)
			if tt.wantedIsErr {
				assert.NotNil(t, err)
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				assert.Nil(t, err)
			}
		})
	}
}

func TestGetBucketPrimarySPIDSuccessfully(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := consensus.NewMockConsensus(ctrl)
	m.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, vgfID uint32) (*virtualgrouptypes.GlobalVirtualGroupFamily, error) {
			return &virtualgrouptypes.GlobalVirtualGroupFamily{PrimarySpId: 1}, nil
		}).AnyTimes()
	result, err := GetBucketPrimarySPID(context.Background(), m, &storagetypes.BucketInfo{
		Owner:      "mockUser",
		BucketName: "mockBucket",
	})
	assert.Equal(t, uint32(1), result)
	assert.Nil(t, err)
}

func TestGetBucketPrimarySPIDFailed(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := consensus.NewMockConsensus(ctrl)
	m.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, vgfID uint32) (*virtualgrouptypes.GlobalVirtualGroupFamily, error) {
			return nil, errors.New("failed to call rpc")
		}).AnyTimes()
	result, err := GetBucketPrimarySPID(context.Background(), m, &storagetypes.BucketInfo{
		Owner:      "mockUser",
		BucketName: "mockBucket",
	})
	assert.Equal(t, uint32(0), result)
	assert.NotNil(t, err)
}
