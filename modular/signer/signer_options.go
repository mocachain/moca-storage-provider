package signer

import (
	"context"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	coremodule "github.com/mocachain/moca-storage-provider/core/module"
	"github.com/mocachain/moca-storage-provider/store/sqldb"
	"github.com/mocachain/moca-storage-provider/util"
	"github.com/mocachain/moca/v2/sdk/types"
)

const (
	// The Default*GasLimit values are upper bounds, not the gas a transaction is sent
	// with: every EVM transaction is sent with the node's estimate plus
	// EvmGasMarginPercent (see CreateTxOpts). They sit at about twice the largest gas
	// measured on a moca v2 network, for example 432k-473k for a seal, 109k to create a
	// global virtual group and 121k to complete a swap-out, so they only stop an estimate
	// that has gone far off.
	DefaultSealGasLimit                         = 1_000_000
	DefaultSealFeeAmount                        = 6000000000000
	DefaultRejectSealGasLimit                   = 300_000
	DefaultRejectSealFeeAmount                  = 60000000000000
	DefaultDiscontinueBucketGasLimit            = 300_000
	DefaultDiscontinueBucketFeeAmount           = 12000000000000
	DefaultCreateGlobalVirtualGroupGasLimit     = 500_000
	DefaultCreateGlobalVirtualGroupFeeAmount    = 6000000000000
	DefaultCompleteMigrateBucketGasLimit        = 1_000_000
	DefaultCompleteMigrateBucketFeeAmount       = 6000000000000
	DefaultUpdateSPPriceGasLimit                = 300_000
	DefaultUpdateSPPriceFeeAmount               = 60000000000000
	DefaultSwapOutGasLimit                      = 300_000
	DefaultSwapOutFeeAmount                     = 60000000000000
	DefaultCompleteSwapOutGasLimit              = 500_000
	DefaultCompleteSwapOutFeeAmount             = 60000000000000
	DefaultSPExitGasLimit                       = 300_000
	DefaultSPExitFeeAmount                      = 60000000000000
	DefaultCompleteSPExitGasLimit               = 300_000
	DefaultCompleteSPExitFeeAmount              = 60000000000000
	DefaultRejectMigrateBucketGasLimit          = 500_000
	DefaultRejectMigrateBucketFeeAmount         = 60000000000000
	DefaultDepositGasLimit                      = 300_000
	DefaultDepositFeeAmount                     = 60000000000000
	DefaultDeleteGlobalVirtualGroupGasLimit     = 500_000
	DefaultDeleteGlobalVirtualGroupFeeAmount    = 60000000000000
	DefaultDelegateCreateObjectGasLimit         = 500_000
	DefaultDelegateCreateObjectFeeAmount        = 60000000000000
	DefaultDelegateUpdateObjectContentGasLimit  = 500_000
	DefaultDelegateUpdateObjectContentFeeAmount = 60000000000000
	DefaultReserveSwapInGasLimit                = 500_000
	DefaultReserveSwapInFeeAmount               = 60000000000000
	DefaultCompleteSwapInGasLimit               = 500_000
	DefaultCompleteSwapInFeeAmount              = 60000000000000
	DefaultCancelSwapInGasLimit                 = 500_000
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
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.Chain.MaxEvmGasPriceWei == 0 {
		cfg.Chain.MaxEvmGasPriceWei = gfspconfig.DefaultMaxEvmGasPriceWei
	}
	if cfg.Chain.EvmGasMarginPercent == 0 {
		cfg.Chain.EvmGasMarginPercent = gfspconfig.DefaultEvmGasMarginPercent
	}
	if cfg.Chain.EvmGasMarginPercent > gfspconfig.MaxEvmGasMarginPercent {
		return fmt.Errorf("EvmGasMarginPercent %d is above %d", cfg.Chain.EvmGasMarginPercent, gfspconfig.MaxEvmGasMarginPercent)
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
	var err error
	if cfg.SpAccount.OperatorPrivateKey, err = util.SecretFromEnv(SpOperatorPrivKey, cfg.SpAccount.OperatorPrivateKey); err != nil {
		return err
	}
	if cfg.SpAccount.SealPrivateKey, err = util.SecretFromEnv(SpSealPrivKey, cfg.SpAccount.SealPrivateKey); err != nil {
		return err
	}
	if cfg.SpAccount.BlsPrivateKey, err = util.SecretFromEnv(SpBlsPrivKey, cfg.SpAccount.BlsPrivateKey); err != nil {
		return err
	}
	if cfg.SpAccount.ApprovalPrivateKey, err = util.SecretFromEnv(SpApprovalPrivKey, cfg.SpAccount.ApprovalPrivateKey); err != nil {
		return err
	}
	if cfg.SpAccount.GcPrivateKey, err = util.SecretFromEnv(SpGcPrivKey, cfg.SpAccount.GcPrivateKey); err != nil {
		return err
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
		gasInfo, cfg.Chain.MaxEvmGasPriceWei, cfg.SpAccount.OperatorPrivateKey,
		cfg.SpAccount.SealPrivateKey, cfg.SpAccount.ApprovalPrivateKey, cfg.SpAccount.GcPrivateKey, cfg.SpAccount.BlsPrivateKey)
	if err != nil {
		return err
	}
	client.evmGasMarginPercent = cfg.Chain.EvmGasMarginPercent
	if err = attachSignerEvmStore(client, cfg); err != nil {
		return err
	}
	signer.client = client
	client.signer = signer
	return nil
}

// attachSignerEvmStore opens the signer's own connection to the SP DB for the
// EVM state tables. The signer does not migrate them: 'signer-evm migrate'
// does, with the migration account. Without the DB the signer does not start
// (D1: EVM submission fails closed).
func attachSignerEvmStore(client *MocaChainSignClient, cfg *gfspconfig.GfSpConfig) error {
	dbCfg := cfg.SpDB
	store, err := sqldb.NewSignerEvmStore(&dbCfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), evmStateWriteTimeout)
	defer cancel()
	if err = store.CheckSchema(ctx); err != nil {
		_ = store.Close()
		return err
	}
	if err = client.attachEvmState(newSQLEvmStateStore(store.DB()), cfg.Chain.ChainID, 0); err != nil {
		_ = store.Close()
		return err
	}
	client.evmCloser = store.Close
	return nil
}
