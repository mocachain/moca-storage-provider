package signer

import (
	"fmt"
	"os"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/evmos/evmos/v12/sdk/types"
	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	coremodule "github.com/mocachain/moca-storage-provider/core/module"
)

const (
	DefaultSealGasLimit                         = 1200 // fix gas limit for MsgSealObject is 1200
	DefaultSealFeeAmount                        = 6000000000000
	DefaultRejectSealGasLimit                   = 12000 // fix gas limit for MsgRejectSealObject is 12000
	DefaultRejectSealFeeAmount                  = 60000000000000
	DefaultDiscontinueBucketGasLimit            = 2400 // fix gas limit for MsgDiscontinueBucket is 2400
	DefaultDiscontinueBucketFeeAmount           = 12000000000000
	DefaultCreateGlobalVirtualGroupGasLimit     = 1200 // fix gas limit for MsgCreateGlobalVirtualGroup is 1200
	DefaultCreateGlobalVirtualGroupFeeAmount    = 6000000000000
	DefaultCompleteMigrateBucketGasLimit        = 1200 // fix gas limit for MsgCompleteMigrateBucket is 1200
	DefaultCompleteMigrateBucketFeeAmount       = 6000000000000
	DefaultUpdateSPPriceGasLimit                = 12000
	DefaultUpdateSPPriceFeeAmount               = 60000000000000
	DefaultSwapOutGasLimit                      = 12000
	DefaultSwapOutFeeAmount                     = 60000000000000
	DefaultCompleteSwapOutGasLimit              = 12000
	DefaultCompleteSwapOutFeeAmount             = 60000000000000
	DefaultSPExitGasLimit                       = 12000
	DefaultSPExitFeeAmount                      = 60000000000000
	DefaultCompleteSPExitGasLimit               = 12000
	DefaultCompleteSPExitFeeAmount              = 60000000000000
	DefaultRejectMigrateBucketGasLimit          = 12000
	DefaultRejectMigrateBucketFeeAmount         = 60000000000000
	DefaultDepositGasLimit                      = 12000
	DefaultDepositFeeAmount                     = 60000000000000
	DefaultDeleteGlobalVirtualGroupGasLimit     = 12000
	DefaultDeleteGlobalVirtualGroupFeeAmount    = 60000000000000
	DefaultDelegateCreateObjectGasLimit         = 12000
	DefaultDelegateCreateObjectFeeAmount        = 60000000000000
	DefaultDelegateUpdateObjectContentGasLimit  = 12000
	DefaultDelegateUpdateObjectContentFeeAmount = 60000000000000
	DefaultReserveSwapInGasLimit                = 12000
	DefaultReserveSwapInFeeAmount               = 60000000000000
	DefaultCompleteSwapInGasLimit               = 12000
	DefaultCompleteSwapInFeeAmount              = 60000000000000
	DefaultCancelSwapInGasLimit                 = 12000
	DefaultCancelSwapInFeeAmount                = 60000000000000

	// SpOperatorPrivKey defines env variable name for sp operator private key
	SpOperatorPrivKey = "SIGNER_OPERATOR_PRIV_KEY"
	// SpApprovalPrivKey defines env variable name for sp approval private key
	SpApprovalPrivKey = "SIGNER_APPROVAL_PRIV_KEY"
	// SpSealPrivKey defines env variable name for sp seal private key
	SpSealPrivKey = "SIGNER_SEAL_PRIV_KEY"
	// SpBlsPrivKey defines env variable name for sp bls private key
	SpBlsPrivKey = "SIGNER_BLS_PRIV_KEY"
	// SpGcPrivKey defines env variable name for sp gc private key
	SpGcPrivKey = "SIGNER_GC_PRIV_KEY"
)

func NewSignModular(app *gfspapp.GfSpBaseApp, cfg *gfspconfig.GfSpConfig) (coremodule.Modular, error) {
	signer := &SignModular{baseApp: app}
	if err := DefaultSignerOptions(signer, cfg); err != nil {
		return nil, err
	}
	return signer, nil
}

func DefaultSignerOptions(signer *SignModular, cfg *gfspconfig.GfSpConfig) error {
	if len(cfg.Chain.ChainAddress) == 0 {
		return fmt.Errorf("chain address missing")
	}
	if cfg.Chain.SealGasLimit == 0 {
		cfg.Chain.SealGasLimit = DefaultSealGasLimit
	}
	if cfg.Chain.SealFeeAmount == 0 {
		cfg.Chain.SealFeeAmount = DefaultSealFeeAmount
	}
	if cfg.Chain.RejectSealGasLimit == 0 {
		cfg.Chain.RejectSealGasLimit = DefaultRejectSealGasLimit
	}
	if cfg.Chain.RejectSealFeeAmount == 0 {
		cfg.Chain.RejectSealFeeAmount = DefaultRejectSealFeeAmount
	}
	if cfg.Chain.DiscontinueBucketGasLimit == 0 {
		cfg.Chain.DiscontinueBucketGasLimit = DefaultDiscontinueBucketGasLimit
	}
	if cfg.Chain.DiscontinueBucketFeeAmount == 0 {
		cfg.Chain.DiscontinueBucketFeeAmount = DefaultDiscontinueBucketFeeAmount
	}
	if cfg.Chain.CreateGlobalVirtualGroupGasLimit == 0 {
		cfg.Chain.CreateGlobalVirtualGroupGasLimit = DefaultCreateGlobalVirtualGroupGasLimit
	}
	if cfg.Chain.CreateGlobalVirtualGroupFeeAmount == 0 {
		cfg.Chain.CreateGlobalVirtualGroupFeeAmount = DefaultCreateGlobalVirtualGroupFeeAmount
	}
	if cfg.Chain.CompleteMigrateBucketGasLimit == 0 {
		cfg.Chain.CompleteMigrateBucketGasLimit = DefaultCompleteMigrateBucketGasLimit
	}
	if cfg.Chain.CompleteMigrateBucketFeeAmount == 0 {
		cfg.Chain.CompleteMigrateBucketFeeAmount = DefaultCompleteMigrateBucketFeeAmount
	}
	if cfg.Chain.UpdateSPPriceGasLimit == 0 {
		cfg.Chain.UpdateSPPriceGasLimit = DefaultUpdateSPPriceGasLimit
	}
	if cfg.Chain.UpdateSPPriceFeeAmount == 0 {
		cfg.Chain.UpdateSPPriceFeeAmount = DefaultUpdateSPPriceFeeAmount
	}
	if cfg.Chain.SwapOutGasLimit == 0 {
		cfg.Chain.SwapOutGasLimit = DefaultSwapOutGasLimit
	}
	if cfg.Chain.SwapOutFeeAmount == 0 {
		cfg.Chain.SwapOutFeeAmount = DefaultSwapOutFeeAmount
	}
	if cfg.Chain.CompleteSwapOutGasLimit == 0 {
		cfg.Chain.CompleteSwapOutGasLimit = DefaultCompleteSwapOutGasLimit
	}
	if cfg.Chain.CompleteSwapOutFeeAmount == 0 {
		cfg.Chain.CompleteSwapOutFeeAmount = DefaultCompleteSwapOutFeeAmount
	}
	if cfg.Chain.SPExitGasLimit == 0 {
		cfg.Chain.SPExitGasLimit = DefaultSPExitGasLimit
	}
	if cfg.Chain.SPExitFeeAmount == 0 {
		cfg.Chain.SPExitFeeAmount = DefaultSPExitFeeAmount
	}
	if cfg.Chain.CompleteSPExitGasLimit == 0 {
		cfg.Chain.CompleteSPExitGasLimit = DefaultCompleteSPExitGasLimit
	}
	if cfg.Chain.CompleteSPExitFeeAmount == 0 {
		cfg.Chain.CompleteSPExitFeeAmount = DefaultCompleteSPExitFeeAmount
	}
	if cfg.Chain.RejectMigrateBucketGasLimit == 0 {
		cfg.Chain.RejectMigrateBucketGasLimit = DefaultRejectMigrateBucketGasLimit
	}
	if cfg.Chain.RejectMigrateBucketFeeAmount == 0 {
		cfg.Chain.RejectMigrateBucketFeeAmount = DefaultRejectMigrateBucketFeeAmount
	}
	if cfg.Chain.DepositGasLimit == 0 {
		cfg.Chain.DepositGasLimit = DefaultDepositGasLimit
	}
	if cfg.Chain.DepositFeeAmount == 0 {
		cfg.Chain.DepositFeeAmount = DefaultDepositFeeAmount
	}
	if cfg.Chain.DeleteGlobalVirtualGroupGasLimit == 0 {
		cfg.Chain.DeleteGlobalVirtualGroupGasLimit = DefaultDeleteGlobalVirtualGroupGasLimit
	}
	if cfg.Chain.DeleteGlobalVirtualGroupFeeAmount == 0 {
		cfg.Chain.DeleteGlobalVirtualGroupFeeAmount = DefaultDeleteGlobalVirtualGroupFeeAmount
	}
	if cfg.Chain.DelegateCreateObjectGasLimit == 0 {
		cfg.Chain.DelegateCreateObjectGasLimit = DefaultDelegateCreateObjectGasLimit
	}
	if cfg.Chain.DelegateCreateObjectFeeAmount == 0 {
		cfg.Chain.DelegateCreateObjectFeeAmount = DefaultDelegateCreateObjectFeeAmount
	}
	if cfg.Chain.DelegateUpdateObjectContentGasLimit == 0 {
		cfg.Chain.DelegateUpdateObjectContentGasLimit = DefaultDelegateUpdateObjectContentGasLimit
	}
	if cfg.Chain.DelegateUpdateObjectContentFeeAmount == 0 {
		cfg.Chain.DelegateUpdateObjectContentFeeAmount = DefaultDelegateUpdateObjectContentFeeAmount
	}
	if cfg.Chain.ReserveSwapInGasLimit == 0 {
		cfg.Chain.ReserveSwapInGasLimit = DefaultReserveSwapInGasLimit
	}
	if cfg.Chain.ReserveSwapInFeeAmount == 0 {
		cfg.Chain.ReserveSwapInFeeAmount = DefaultReserveSwapInFeeAmount
	}
	if cfg.Chain.CompleteSwapInGasLimit == 0 {
		cfg.Chain.CompleteSwapInGasLimit = DefaultCompleteSwapInGasLimit
	}
	if cfg.Chain.CompleteSwapInFeeAmount == 0 {
		cfg.Chain.CompleteSwapInFeeAmount = DefaultCompleteSwapInFeeAmount
	}
	if cfg.Chain.CancelSwapInGasLimit == 0 {
		cfg.Chain.CancelSwapInGasLimit = DefaultCancelSwapInGasLimit
	}
	if cfg.Chain.CancelSwapInFeeAmount == 0 {
		cfg.Chain.CancelSwapInFeeAmount = DefaultCancelSwapInFeeAmount
	}
	if val, ok := os.LookupEnv(SpOperatorPrivKey); ok {
		cfg.SpAccount.OperatorPrivateKey = val
	}
	if val, ok := os.LookupEnv(SpSealPrivKey); ok {
		cfg.SpAccount.SealPrivateKey = val
	}
	if val, ok := os.LookupEnv(SpBlsPrivKey); ok {
		cfg.SpAccount.BlsPrivateKey = val
	}
	if val, ok := os.LookupEnv(SpApprovalPrivKey); ok {
		cfg.SpAccount.ApprovalPrivateKey = val
	}
	if val, ok := os.LookupEnv(SpGcPrivKey); ok {
		cfg.SpAccount.GcPrivateKey = val
	}

	gasInfo := make(map[GasInfoType]GasInfo)
	gasInfo[Seal] = GasInfo{
		GasLimit:  cfg.Chain.SealGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.SealFeeAmount)))),
	}
	gasInfo[RejectSeal] = GasInfo{
		GasLimit:  cfg.Chain.RejectSealGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.RejectSealFeeAmount)))),
	}
	gasInfo[DiscontinueBucket] = GasInfo{
		GasLimit:  cfg.Chain.DiscontinueBucketGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.DiscontinueBucketFeeAmount)))),
	}
	gasInfo[CreateGlobalVirtualGroup] = GasInfo{
		GasLimit:  cfg.Chain.CreateGlobalVirtualGroupGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.CreateGlobalVirtualGroupFeeAmount)))),
	}
	gasInfo[CompleteMigrateBucket] = GasInfo{
		GasLimit:  cfg.Chain.CompleteMigrateBucketGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.CompleteMigrateBucketFeeAmount)))),
	}
	gasInfo[UpdateSPPrice] = GasInfo{
		GasLimit:  cfg.Chain.UpdateSPPriceGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.UpdateSPPriceFeeAmount)))),
	}
	gasInfo[SwapOut] = GasInfo{
		GasLimit:  cfg.Chain.SwapOutGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.SwapOutFeeAmount)))),
	}
	gasInfo[CompleteSwapOut] = GasInfo{
		GasLimit:  cfg.Chain.CompleteSwapOutGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.CompleteSwapOutFeeAmount)))),
	}
	gasInfo[SPExit] = GasInfo{
		GasLimit:  cfg.Chain.SPExitGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.SPExitFeeAmount)))),
	}
	gasInfo[CompleteSPExit] = GasInfo{
		GasLimit:  cfg.Chain.CompleteSPExitGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.CompleteSPExitFeeAmount)))),
	}
	gasInfo[RejectMigrateBucket] = GasInfo{
		GasLimit:  cfg.Chain.RejectMigrateBucketGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.RejectMigrateBucketFeeAmount)))),
	}
	gasInfo[Deposit] = GasInfo{
		GasLimit:  cfg.Chain.DepositGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.DepositFeeAmount)))),
	}
	gasInfo[DeleteGlobalVirtualGroup] = GasInfo{
		GasLimit:  cfg.Chain.DeleteGlobalVirtualGroupGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.DeleteGlobalVirtualGroupFeeAmount)))),
	}
	gasInfo[DelegateCreateObject] = GasInfo{
		GasLimit:  cfg.Chain.DelegateCreateObjectGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.DelegateCreateObjectFeeAmount)))),
	}
	gasInfo[DelegateUpdateObjectContent] = GasInfo{
		GasLimit:  cfg.Chain.DelegateUpdateObjectContentGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.DelegateUpdateObjectContentFeeAmount)))),
	}
	gasInfo[ReserveSwapIn] = GasInfo{
		GasLimit:  cfg.Chain.ReserveSwapInGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.ReserveSwapInFeeAmount)))),
	}
	gasInfo[CompleteSwapIn] = GasInfo{
		GasLimit:  cfg.Chain.CompleteSwapInGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.CompleteSwapInFeeAmount)))),
	}
	gasInfo[CancelSwapIn] = GasInfo{
		GasLimit:  cfg.Chain.CancelSwapInGasLimit,
		FeeAmount: sdk.NewCoins(sdk.NewCoin(types.Denom, math.NewInt(int64(cfg.Chain.CancelSwapInFeeAmount)))),
	}

	if len(cfg.Chain.ChainAddress) == 0 {
		return fmt.Errorf("chain address not configured")
	}
	if len(cfg.Chain.RpcAddress) == 0 {
		return fmt.Errorf("rpc address not configured")
	}

	client, err := NewMocaChainSignClient(cfg.Chain.ChainAddress[0], cfg.Chain.RpcAddress[0], cfg.Chain.ChainID,
		gasInfo, cfg.SpAccount.OperatorPrivateKey, cfg.SpAccount.FundingPrivateKey,
		cfg.SpAccount.SealPrivateKey, cfg.SpAccount.ApprovalPrivateKey, cfg.SpAccount.GcPrivateKey, cfg.SpAccount.BlsPrivateKey)
	if err != nil {
		return err
	}
	signer.client = client
	client.signer = signer
	return nil
}
