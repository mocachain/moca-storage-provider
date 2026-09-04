package signer

import (
	"context"
	"errors"
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

func setupFundValidationSigner(t *testing.T, params *virtualgrouptypes.Params, paramsErr error) *SignModular {
	ctrl := gomock.NewController(t)
	con := consensus.NewMockConsensus(ctrl)
	con.EXPECT().QueryVirtualGroupParams(gomock.Any()).Return(params, paramsErr).AnyTimes()
	s := &SignModular{baseApp: &gfspapp.GfSpBaseApp{}}
	s.baseApp.SetConsensus(con)
	return s
}

func boundedParams() *virtualgrouptypes.Params {
	return &virtualgrouptypes.Params{
		DepositDenom:          "amoca",
		GvgStakingPerBytes:    sdkmath.NewInt(16000),
		MaxStoreSizePerFamily: 64 * 1024 * 1024 * 1024 * 1024,
	}
}

// max deposit for the params above: 16000 * 64TiB
func maxFamilyStake() sdkmath.Int {
	return sdkmath.NewInt(16000).Mul(sdkmath.NewIntFromUint64(64 * 1024 * 1024 * 1024 * 1024))
}

func TestSignModular_CreateGlobalVirtualGroupRejectsWrongDenomDeposit(t *testing.T) {
	s := setupFundValidationSigner(t, boundedParams(), nil)
	_, err := s.CreateGlobalVirtualGroup(context.TODO(), &virtualgrouptypes.MsgCreateGlobalVirtualGroup{
		Deposit: sdk.Coin{Denom: "notmoca", Amount: sdkmath.NewInt(1)},
	})
	assert.ErrorIs(t, err, ErrUnexpectedFundDeposit)
}

func TestSignModular_CreateGlobalVirtualGroupRejectsNonPositiveDeposit(t *testing.T) {
	s := setupFundValidationSigner(t, boundedParams(), nil)
	_, err := s.CreateGlobalVirtualGroup(context.TODO(), &virtualgrouptypes.MsgCreateGlobalVirtualGroup{
		Deposit: sdk.Coin{Denom: "amoca", Amount: sdkmath.NewInt(0)},
	})
	assert.ErrorIs(t, err, ErrUnexpectedFundDeposit)
}

func TestSignModular_CreateGlobalVirtualGroupRejectsDepositBeyondFamilyStake(t *testing.T) {
	s := setupFundValidationSigner(t, boundedParams(), nil)
	_, err := s.CreateGlobalVirtualGroup(context.TODO(), &virtualgrouptypes.MsgCreateGlobalVirtualGroup{
		Deposit: sdk.Coin{Denom: "amoca", Amount: maxFamilyStake().AddRaw(1)},
	})
	assert.ErrorIs(t, err, ErrUnexpectedFundDeposit)
}

func TestSignModular_DepositRejectsWrongDenom(t *testing.T) {
	s := setupFundValidationSigner(t, boundedParams(), nil)
	_, err := s.Deposit(context.TODO(), &virtualgrouptypes.MsgDeposit{
		Deposit: sdk.Coin{Denom: "notmoca", Amount: sdkmath.NewInt(1)},
	})
	assert.ErrorIs(t, err, ErrUnexpectedFundDeposit)
}

func TestSignModular_DepositRejectsWhenParamsUnavailable(t *testing.T) {
	s := setupFundValidationSigner(t, nil, errors.New("consensus down"))
	_, err := s.Deposit(context.TODO(), &virtualgrouptypes.MsgDeposit{
		Deposit: sdk.Coin{Denom: "amoca", Amount: sdkmath.NewInt(1)},
	})
	assert.ErrorContains(t, err, "consensus down")
}

func TestSignModular_ValidateGVGDepositAcceptsBoundedDeposit(t *testing.T) {
	s := setupFundValidationSigner(t, boundedParams(), nil)
	err := s.validateGVGDeposit(context.TODO(), sdk.Coin{Denom: "amoca", Amount: maxFamilyStake()})
	assert.Nil(t, err)
}

func TestSignModular_ValidateGVGDepositSkipsCapOnDegenerateParams(t *testing.T) {
	s := setupFundValidationSigner(t, &virtualgrouptypes.Params{DepositDenom: "amoca"}, nil)
	err := s.validateGVGDeposit(context.TODO(), sdk.Coin{Denom: "amoca", Amount: sdkmath.NewInt(1)})
	assert.Nil(t, err)
}
