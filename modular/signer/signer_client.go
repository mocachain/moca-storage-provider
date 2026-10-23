package signer

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	stderrors "errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkErrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/gogoproto/proto"
	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/grpc"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/mocachain/moca-storage-provider/pkg/log"
	"github.com/mocachain/moca/v2/precompiles/storage"
	"github.com/mocachain/moca/v2/precompiles/virtualgroup"
	"github.com/mocachain/moca/v2/sdk/client"
	"github.com/mocachain/moca/v2/sdk/keys"
	ctypes "github.com/mocachain/moca/v2/sdk/types"
	evmostypes "github.com/mocachain/moca/v2/types"
	"github.com/mocachain/moca/v2/types/common"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

// test seams for chain interactions
var (
	waitForEvmTxFn      = client.WaitForEvmTx
	getCosmosNonceFn    = (*client.MocaClient).GetNonce
	broadcastCosmosTxFn = (*client.MocaClient).BroadcastTx
	getNonceOnChainFn   = waitNextBlockAndGetNonce
	sendEvmTxFn         = func(ctx context.Context, evmClient *ethclient.Client, tx *types.Transaction) error {
		return evmClient.SendTransaction(ctx, tx)
	}
	transactionReceiptFn = func(ctx context.Context, evmClient *ethclient.Client,
		txHash ethcmn.Hash,
	) (*types.Receipt, error) {
		return evmClient.TransactionReceipt(ctx, txHash)
	}
)

// SignType is the type of msg signature
type SignType string

// GasInfoType is the type of gas info
type GasInfoType string

const (
	// SignOperator is the type of signature signed by the operator account
	SignOperator SignType = "operator"

	// SignSeal is the type of signature signed by the seal account
	SignSeal SignType = "seal"

	// SignApproval is the type of signature signed by the approval account
	SignApproval SignType = "approval"

	// SignGc is the type of signature signed by the gc account
	SignGc SignType = "gc"

	// BroadcastTxRetry defines the max retry for broadcasting tx on-chain
	BroadcastTxRetry = 3

	Seal                        GasInfoType = "Seal"
	RejectSeal                  GasInfoType = "RejectSeal"
	DelegateCreateObject        GasInfoType = "DelegateCreateObject"
	DelegateUpdateObjectContent GasInfoType = "DelegateUpdateObjectContent"
	DiscontinueBucket           GasInfoType = "DiscontinueBucket"
	CreateGlobalVirtualGroup    GasInfoType = "CreateGlobalVirtualGroup"
	DeleteGlobalVirtualGroup    GasInfoType = "DeleteGlobalVirtualGroup"
	CompleteMigrateBucket       GasInfoType = "CompleteMigrateBucket"
	RejectMigrateBucket         GasInfoType = "RejectMigrateBucket"
	SwapOut                     GasInfoType = "SwapOut"
	ReserveSwapIn               GasInfoType = "ReserveSwapIn"
	CancelSwapIn                GasInfoType = "CancelSwapIn"
	CompleteSwapIn              GasInfoType = "CompleteSwapIn"
	CompleteSwapOut             GasInfoType = "CompleteSwapOut"
	SPExit                      GasInfoType = "SPExit"
	CompleteSPExit              GasInfoType = "CompleteSPExit"
	UpdateSPPrice               GasInfoType = "UpdateSPPrice"
	Deposit                     GasInfoType = "Deposit"
)

// Nonce-related error message constants from various dependencies.
// These are string patterns returned via JSON-RPC from the chain node.
const (
	// From go-ethereum v1.15.5 - core/error.go: ErrNonceTooLow / ErrNonceTooHigh
	errMsgNonceTooLow  = "nonce too low"
	errMsgNonceTooHigh = "nonce too high"

	// From cosmos/evm ante/evm/09_increment_sequence.go: "invalid nonce; got N, expected M".
	// "replacement transaction underpriced" is deliberately absent: it means the
	// node already holds a transaction at this nonce, possibly this one.
	errMsgInvalidNonce = "invalid nonce"

	// From cosmos/evm mempool/errors.go: ErrNonceLow / ErrNonceGap, wrapped by
	// ante/evm/09_increment_sequence.go.
	errMsgNonceLowerThanAccount  = "tx nonce is lower than account nonce"
	errMsgNonceHigherThanAccount = "tx nonce is higher than account nonce"
)

// JSON-RPC error codes that need special handling when classifying an
// eth_sendRawTransaction failure.
const (
	evmRPCCodeInvalidParams = -32602
	evmRPCCodeInternal      = -32603
	evmRPCCodeTimeout       = -32002
)

// evmNotAdmittedReasons are node refusals, other than nonce mismatches, that
// happen before the transaction is admitted. Re-signing does not fix them.
var evmNotAdmittedReasons = []string{
	"failed to validate transaction",
	"failed to build cosmos tx",
	"failed to encode transaction",
	"insufficient funds",
	"insufficient fee",
	"intrinsic gas too low",
}

// evmResendTimeout bounds each re-broadcast of identical bytes after an
// ambiguous submit.
const evmResendTimeout = 10 * time.Second

type GasInfo struct {
	GasLimit  uint64
	FeeAmount sdk.Coins
}

// MocaChainSignClient the moca chain client
type MocaChainSignClient struct {
	signer *SignModular

	opLock   sync.Mutex
	sealLock sync.Mutex
	gcLock   sync.Mutex

	gasInfo          map[GasInfoType]GasInfo
	maxEvmGasPrice   *big.Int
	mocaClients      map[SignType]*client.MocaClient
	evmPrivateKeys   map[SignType]*ecdsa.PrivateKey
	evmClient        *ethclient.Client
	operatorAccNonce uint64
	sealAccNonce     uint64
	gcAccNonce       uint64
	blsKm            keys.KeyManager
	// evm is the persisted EVM submission state; nil refuses every EVM
	// transaction.
	evm       *evmState
	evmCloser func() error
}

// NewMocaChainSignClient return the MocaChainSignClient instance
func NewMocaChainSignClient(rpcAddr, evmRpcAddr, chainID string, gasInfo map[GasInfoType]GasInfo, maxEvmGasPrice uint64, operatorPrivateKey,
	sealPrivateKey, approvalPrivateKey, gcPrivateKey string, blsPrivKey string,
) (*MocaChainSignClient, error) {
	// init clients
	// TODO: Get private key from KMS(AWS, GCP, Azure, Aliyun)
	operatorKM, err := keys.NewPrivateKeyManager(operatorPrivateKey)
	if err != nil {
		log.Errorw("failed to new operator private key manager", "error", err)
		return nil, err
	}

	// creat chain client
	evmClient, err := ethclient.Dial(evmRpcAddr)
	if err != nil {
		log.Errorw("failed to new a evm client", "error", err)
		return nil, err
	}

	operatorClient, err := client.NewMocaClient(rpcAddr, evmRpcAddr, chainID, client.WithKeyManager(operatorKM))
	if err != nil {
		log.Errorw("failed to new operator moca client", "error", err)
		return nil, err
	}
	operatorAccNonce, err := operatorClient.GetNonce(context.Background())
	if err != nil {
		log.Errorw("failed to get operator nonce", "error", err)
		return nil, err
	}

	blsKM, err := keys.NewBlsPrivateKeyManager(blsPrivKey)
	if err != nil {
		log.Errorw("failed to new bls private key manager", "error", err)
		return nil, err
	}

	sealKM, err := keys.NewPrivateKeyManager(sealPrivateKey)
	if err != nil {
		log.Errorw("failed to new seal private key manager", "error", err)
		return nil, err
	}
	sealClient, err := client.NewMocaClient(rpcAddr, evmRpcAddr, chainID, client.WithKeyManager(sealKM))
	if err != nil {
		log.Errorw("failed to new seal moca client", "error", err)
		return nil, err
	}
	sealAccNonce, err := sealClient.GetNonce(context.Background())
	if err != nil {
		log.Errorw("failed to get seal nonce", "error", err)
		return nil, err
	}

	approvalKM, err := keys.NewPrivateKeyManager(approvalPrivateKey)
	if err != nil {
		log.Errorw("failed to new approval private key manager", "error", err)
		return nil, err
	}
	approvalClient, err := client.NewMocaClient(rpcAddr, evmRpcAddr, chainID, client.WithKeyManager(approvalKM))
	if err != nil {
		log.Errorw("failed to new approval moca client", "error", err)
		return nil, err
	}

	gcKM, err := keys.NewPrivateKeyManager(gcPrivateKey)
	if err != nil {
		log.Errorw("failed to new gc private key manager", "error", err)
		return nil, err
	}
	gcClient, err := client.NewMocaClient(rpcAddr, evmRpcAddr, chainID, client.WithKeyManager(gcKM))
	if err != nil {
		log.Errorw("failed to new gc moca client", "error", err)
		return nil, err
	}
	gcAccNonce, err := gcClient.GetNonce(context.Background())
	if err != nil {
		log.Errorw("failed to get gc nonce", "error", err)
		return nil, err
	}

	// keep the parsed keys only, the hex encoded material must not be retained
	evmPrivateKeys := make(map[SignType]*ecdsa.PrivateKey, 4)
	for scope, hexKey := range map[SignType]string{
		SignOperator: operatorPrivateKey,
		SignSeal:     sealPrivateKey,
		SignApproval: approvalPrivateKey,
		SignGc:       gcPrivateKey,
	} {
		evmPrivateKey, err := crypto.HexToECDSA(hexKey)
		if err != nil {
			log.Errorw("failed to parse evm private key", "scope", scope, "error", err)
			return nil, err
		}
		evmPrivateKeys[scope] = evmPrivateKey
	}

	mocaClients := map[SignType]*client.MocaClient{
		SignOperator: operatorClient,
		SignSeal:     sealClient,
		SignApproval: approvalClient,
		SignGc:       gcClient,
	}

	return &MocaChainSignClient{
		gasInfo:          gasInfo,
		maxEvmGasPrice:   new(big.Int).SetUint64(maxEvmGasPrice),
		mocaClients:      mocaClients,
		evmPrivateKeys:   evmPrivateKeys,
		sealAccNonce:     sealAccNonce,
		gcAccNonce:       gcAccNonce,
		operatorAccNonce: operatorAccNonce,
		blsKm:            blsKM,
		evmClient:        evmClient,
	}, nil
}

// GetAddr returns the public address of the private key.
func (client *MocaChainSignClient) GetAddr(scope SignType) (sdk.AccAddress, error) {
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		return nil, err
	}
	return km.GetAddr(), nil
}

// Sign returns a msg signature signed by private key.
func (client *MocaChainSignClient) Sign(scope SignType, msg []byte) ([]byte, error) {
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		return nil, err
	}
	return km.Sign(msg)
}

// VerifySignature verifies the signature.
func (client *MocaChainSignClient) VerifySignature(scope SignType, msg, sig []byte) bool {
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		return false
	}
	return evmostypes.VerifySignature(km.GetAddr(), crypto.Keccak256(msg), sig) == nil
}

// SealObject seal the object on the moca chain.
func (client *MocaChainSignClient) SealObject(ctx context.Context, scope SignType,
	sealObject *storagetypes.MsgSealObject,
) (string, error) {
	if sealObject == nil {
		log.CtxError(ctx, "failed to seal object due to pointer dangling")
		return "", ErrDanglingPointer
	}
	ctx = log.WithValue(ctx, log.CtxKeyBucketName, sealObject.GetBucketName())
	ctx = log.WithValue(ctx, log.CtxKeyObjectName, sealObject.GetObjectName())
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.sealLock.Lock()
	defer client.sealLock.Unlock()

	msgSealObject := storagetypes.NewMsgSealObject(km.GetAddr(),
		sealObject.GetBucketName(), sealObject.GetObjectName(), sealObject.GetGlobalVirtualGroupId(),
		sealObject.GetSecondarySpBlsAggSignatures())

	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC

	txOpt := &ctypes.TxOption{
		NoSimulate: false,
		Mode:       &mode,
		GasLimit:   client.gasInfo[Seal].GasLimit,
		FeeAmount:  client.gasInfo[Seal].FeeAmount,
	}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgSealObject}, txOpt, &client.sealAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast seal object tx", "error", err)
		ErrSealObjectOnChain.SetError(fmt.Errorf("failed to broadcast seal object tx, error: %v", err))
		return "", ErrSealObjectOnChain
	}
	client.sealAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast seal object tx", "tx_hash", txHash, "seal_msg", msgSealObject)
	return txHash, nil
}

// SealObjectEvm seal the object on the moca by evm tx.
func (client *MocaChainSignClient) SealObjectEvm(ctx context.Context, scope SignType,
	sealObject *storagetypes.MsgSealObject,
) (string, error) {
	if sealObject == nil {
		log.CtxError(ctx, "failed to seal object due to pointer dangling")
		return "", ErrDanglingPointer
	}
	ctx = log.WithValue(ctx, log.CtxKeyBucketName, sealObject.GetBucketName())
	ctx = log.WithValue(ctx, log.CtxKeyObjectName, sealObject.GetObjectName())
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.sealLock.Lock()
	defer client.sealLock.Unlock()

	msgSealObject := storagetypes.NewMsgSealObject(km.GetAddr(),
		sealObject.GetBucketName(), sealObject.GetObjectName(), sealObject.GetGlobalVirtualGroupId(),
		sealObject.GetSecondarySpBlsAggSignatures())
	operation, err := evmOperationFingerprint(msgSealObject)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrSealObjectOnChain.SetError(resolveErr)
			return pendingHash, ErrSealObjectOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash       string
		nonce        uint64
		nonceErr     error
		broadcastErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.sealAccNonce

		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[Seal].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateStorageSession(client.evmClient, *txOpts, evmostypes.StorageAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.SealObject(
			sealObject.GetBucketName(),
			sealObject.GetObjectName(),
			sealObject.GetGlobalVirtualGroupId(),
			base64.StdEncoding.EncodeToString(sealObject.GetSecondarySpBlsAggSignatures()),
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrSealObjectOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrSealObjectOnChain
			}
			// if nonce mismatch, wait for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get seal account nonce", "error", nonceErr)
				ErrSealObjectOnChain.SetError(fmt.Errorf("failed to get seal account nonce, error: %v", nonceErr))
				return "", ErrSealObjectOnChain
			}
			client.sealAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast seal object tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}

		client.sealAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrSealObjectOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrSealObjectOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrSealObjectOnChain.SetError(fmt.Errorf("failed to broadcast seal object tx, error: %v", broadcastErr))
	return "", ErrSealObjectOnChain
}

// RejectUnSealObject reject seal object on the moca chain.
func (client *MocaChainSignClient) RejectUnSealObject(ctx context.Context, scope SignType,
	rejectObject *storagetypes.MsgRejectSealObject,
) (string, error) {
	if rejectObject == nil {
		log.CtxError(ctx, "failed to reject unseal object due to pointer dangling")
		return "", ErrDanglingPointer
	}
	ctx = log.WithValue(ctx, log.CtxKeyBucketName, rejectObject.GetBucketName())
	ctx = log.WithValue(ctx, log.CtxKeyObjectName, rejectObject.GetObjectName())
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.sealLock.Lock()
	defer client.sealLock.Unlock()

	msgRejectUnSealObject := storagetypes.NewMsgRejectUnsealedObject(km.GetAddr(), rejectObject.GetBucketName(), rejectObject.GetObjectName())
	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC

	txOpt := &ctypes.TxOption{
		NoSimulate: false,
		Mode:       &mode,
		GasLimit:   client.gasInfo[RejectSeal].GasLimit,
		FeeAmount:  client.gasInfo[RejectSeal].FeeAmount,
	}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgRejectUnSealObject}, txOpt, &client.sealAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast reject unseal object", "error", err)
		ErrRejectUnSealObjectOnChain.SetError(fmt.Errorf("failed to broadcast reject unseal object tx, error: %v", err))
		return "", ErrRejectUnSealObjectOnChain
	}
	client.sealAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast reject unseal object tx", "tx_hash", txHash)
	return txHash, nil
}

func (client *MocaChainSignClient) RejectUnSealObjectEvm(ctx context.Context, scope SignType,
	rejectObject *storagetypes.MsgRejectSealObject,
) (string, error) {
	if rejectObject == nil {
		log.CtxError(ctx, "failed to reject unseal object due to pointer dangling")
		return "", ErrDanglingPointer
	}
	ctx = log.WithValue(ctx, log.CtxKeyBucketName, rejectObject.GetBucketName())
	ctx = log.WithValue(ctx, log.CtxKeyObjectName, rejectObject.GetObjectName())
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.sealLock.Lock()
	defer client.sealLock.Unlock()

	msgRejectUnSealObject := storagetypes.NewMsgRejectUnsealedObject(km.GetAddr(), rejectObject.GetBucketName(), rejectObject.GetObjectName())
	operation, err := evmOperationFingerprint(msgRejectUnSealObject)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrRejectUnSealObjectOnChain.SetError(resolveErr)
			return pendingHash, ErrRejectUnSealObjectOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash       string
		nonce        uint64
		nonceErr     error
		broadcastErr error
	)

	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.sealAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[RejectSeal].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateStorageSession(client.evmClient, *txOpts, evmostypes.StorageAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.RejectSealObject(
			rejectObject.GetBucketName(),
			rejectObject.GetObjectName(),
		)
		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrRejectUnSealObjectOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrRejectUnSealObjectOnChain
			}
			// if nonce mismatch, wait for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get seal account nonce", "error", nonceErr)
				ErrRejectUnSealObjectOnChain.SetError(fmt.Errorf("failed to get seal account nonce, error: %v", nonceErr))
				return "", ErrRejectUnSealObjectOnChain
			}
			client.sealAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast reject unseal object", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}

		client.sealAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrRejectUnSealObjectOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrRejectUnSealObjectOnChain
		}

		return txHash, nil
	}
	// failed to broadcast tx
	ErrRejectUnSealObjectOnChain.SetError(fmt.Errorf("failed to broadcast reject unseal object tx, error: %v", broadcastErr))
	return "", ErrRejectUnSealObjectOnChain
}

// DiscontinueBucket stops serving the bucket on the moca chain.
func (client *MocaChainSignClient) DiscontinueBucket(ctx context.Context, scope SignType, discontinueBucket *storagetypes.MsgDiscontinueBucket) (string, error) {
	log.Infow("signer start to discontinue bucket", "scope", scope)
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "err", err)
		return "", ErrSignMsg
	}

	client.gcLock.Lock()
	defer client.gcLock.Unlock()

	msgDiscontinueBucket := storagetypes.NewMsgDiscontinueBucket(km.GetAddr(),
		discontinueBucket.BucketName, discontinueBucket.Reason)
	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC
	txOpt := &ctypes.TxOption{ // allow simulation here to save gas cost
		Mode: &mode,
	}

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgDiscontinueBucket}, txOpt, &client.gcAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast discontinue bucket", "error", err, "discontinue_bucket", msgDiscontinueBucket.String())
		ErrDiscontinueBucketOnChain.SetError(fmt.Errorf("failed to broadcast discontinue bucket, error: %v", err))
		return "", ErrDiscontinueBucketOnChain
	}
	client.gcAccNonce = nonce + 1
	return txHash, nil
}

func (client *MocaChainSignClient) DiscontinueBucketEvm(ctx context.Context, scope SignType, discontinueBucket *storagetypes.MsgDiscontinueBucket) (string, error) {
	log.Infow("signer start to discontinue bucket", "scope", scope)
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "err", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.gcLock.Lock()
	defer client.gcLock.Unlock()
	nonce := client.gcAccNonce

	msgDiscontinueBucket := storagetypes.NewMsgDiscontinueBucket(km.GetAddr(),
		discontinueBucket.BucketName, discontinueBucket.Reason)
	operation, err := evmOperationFingerprint(msgDiscontinueBucket)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrDiscontinueBucketOnChain.SetError(resolveErr)
			return pendingHash, ErrDiscontinueBucketOnChain
		}
		return pendingHash, nil
	}

	txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[DiscontinueBucket].GasLimit, nonce, client.maxEvmGasPrice)
	if err != nil {
		log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
		return "", err
	}
	txOpts.NoSend = true

	session, err := CreateStorageSession(client.evmClient, *txOpts, evmostypes.StorageAddress)
	if err != nil {
		log.CtxErrorw(ctx, "failed to create session", "error", err)
		return "", err
	}

	signedTx, err := session.DiscontinueBucket(
		discontinueBucket.GetBucketName(),
		discontinueBucket.GetReason(),
	)

	if err != nil {
		log.CtxErrorw(ctx, "failed to sign EVM tx", "error", err)
		ErrDiscontinueBucketOnChain.SetError(fmt.Errorf("failed to sign EVM tx: %w", err))
		return "", ErrDiscontinueBucketOnChain
	}
	txHash, err := client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
	if txHash == "" && err != nil {
		if isEvmNonceRejection(err) {
			// if nonce mismatch, wait for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr := client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get gc account nonce", "error", nonceErr)
				ErrDiscontinueBucketOnChain.SetError(fmt.Errorf("failed to get gc account nonce, error: %v", nonceErr))
				return "", ErrDiscontinueBucketOnChain
			}
			client.gcAccNonce = nonce
		}
		log.CtxErrorw(ctx, "failed to broadcast discontinue bucket", "error", err, "discontinue_bucket", msgDiscontinueBucket.String())
		ErrDiscontinueBucketOnChain.SetError(fmt.Errorf("failed to broadcast discontinue bucket, error: %v", err))
		return "", ErrDiscontinueBucketOnChain
	}
	client.gcAccNonce = nonce + 1

	if err != nil {
		log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
		ErrDiscontinueBucketOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
		return txHash, ErrDiscontinueBucketOnChain
	}

	return txHash, nil
}

func (client *MocaChainSignClient) CreateGlobalVirtualGroup(ctx context.Context, scope SignType,
	gvg *virtualgrouptypes.MsgCreateGlobalVirtualGroup,
) (string, error) {
	log.Infow("signer starts to create a new global virtual group", "scope", scope)
	if gvg == nil {
		log.CtxError(ctx, "failed to create virtual group due to pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCreateGlobalVirtualGroup := virtualgrouptypes.NewMsgCreateGlobalVirtualGroup(km.GetAddr(),
		gvg.FamilyId, gvg.GetSecondarySpIds(), gvg.GetDeposit())
	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC

	txOpt := &ctypes.TxOption{
		Mode:      &mode,
		GasLimit:  client.gasInfo[CreateGlobalVirtualGroup].GasLimit,
		FeeAmount: client.gasInfo[CreateGlobalVirtualGroup].FeeAmount,
	}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgCreateGlobalVirtualGroup}, txOpt, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast global virtual group tx", "global_virtual_group",
			msgCreateGlobalVirtualGroup.String(), "error", err)
		ErrCreateGVGOnChain.SetError(fmt.Errorf("failed to broadcast create virtual group tx, error: %v", err))
		return "", ErrCreateGVGOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast create virtual group tx", "tx_hash", txHash,
		"virtual_group_msg", msgCreateGlobalVirtualGroup)
	return txHash, nil
}

func (client *MocaChainSignClient) CreateGlobalVirtualGroupEvm(ctx context.Context, scope SignType,
	gvg *virtualgrouptypes.MsgCreateGlobalVirtualGroup,
) (string, error) {
	log.Infow("signer starts to create a new global virtual group", "scope", scope)
	if gvg == nil {
		log.CtxError(ctx, "failed to create virtual group due to pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCreateGlobalVirtualGroup := virtualgrouptypes.NewMsgCreateGlobalVirtualGroup(km.GetAddr(),
		gvg.FamilyId, gvg.GetSecondarySpIds(), gvg.GetDeposit())
	operation, err := evmOperationFingerprint(msgCreateGlobalVirtualGroup)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrCreateGVGOnChain.SetError(resolveErr)
			return pendingHash, ErrCreateGVGOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash       string
		nonce        uint64
		nonceErr     error
		broadcastErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[CreateGlobalVirtualGroup].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		deposit := virtualgroup.Coin{
			Denom:  gvg.GetDeposit().Denom,
			Amount: gvg.GetDeposit().Amount.BigInt(),
		}
		signedTx, err := session.CreateGlobalVirtualGroup(
			gvg.FamilyId,
			gvg.GetSecondarySpIds(),
			deposit,
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrCreateGVGOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrCreateGVGOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", nonceErr)
				ErrCreateGVGOnChain.SetError(fmt.Errorf("failed to get approval account nonce, error: %v", err))
				return "", ErrCreateGVGOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast global virtual group tx", "global_virtual_group",
				msgCreateGlobalVirtualGroup.String(), "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrCreateGVGOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrCreateGVGOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrCreateGVGOnChain.SetError(fmt.Errorf("failed to broadcast create virtual group tx, error: %v", broadcastErr))
	return "", ErrCreateGVGOnChain
}

func (client *MocaChainSignClient) CompleteMigrateBucket(ctx context.Context, scope SignType,
	migrateBucket *storagetypes.MsgCompleteMigrateBucket,
) (string, error) {
	log.Infow("signer starts to complete migrate bucket", "scope", scope)
	if migrateBucket == nil {
		log.CtxError(ctx, "complete migrate bucket msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCompleteMigrateBucket := storagetypes.NewMsgCompleteMigrateBucket(km.GetAddr(), migrateBucket.GetBucketName(),
		migrateBucket.GetGlobalVirtualGroupFamilyId(), migrateBucket.GetGvgMappings())

	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC

	txOpt := &ctypes.TxOption{Mode: &mode}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgCompleteMigrateBucket}, txOpt, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast complete migrate bucket tx", "error", err)
		ErrCompleteMigrateBucketOnChain.SetError(fmt.Errorf("failed to broadcast complete migrate bucket, error: %v", err))
		return "", ErrCompleteMigrateBucketOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast complete migrate bucket tx", "tx_hash", txHash, "seal_msg", msgCompleteMigrateBucket)
	return txHash, nil
}

func (client *MocaChainSignClient) CompleteMigrateBucketEvm(ctx context.Context, scope SignType,
	migrateBucket *storagetypes.MsgCompleteMigrateBucket,
) (string, error) {
	log.Infow("signer starts to complete migrate bucket", "scope", scope)
	if migrateBucket == nil {
		log.CtxError(ctx, "complete migrate bucket msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCompleteMigrateBucket := storagetypes.NewMsgCompleteMigrateBucket(km.GetAddr(), migrateBucket.GetBucketName(),
		migrateBucket.GetGlobalVirtualGroupFamilyId(), migrateBucket.GetGvgMappings())
	operation, err := evmOperationFingerprint(msgCompleteMigrateBucket)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrCompleteMigrateBucketOnChain.SetError(resolveErr)
			return pendingHash, ErrCompleteMigrateBucketOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash       string
		nonce        uint64
		nonceErr     error
		broadcastErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[CompleteMigrateBucket].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateStorageSession(client.evmClient, *txOpts, evmostypes.StorageAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		gvgMappings := make([]storage.GVGMapping, 0)
		for _, gvgMapping := range migrateBucket.GetGvgMappings() {
			gvgMappings = append(gvgMappings, storage.GVGMapping{
				SrcGlobalVirtualGroupId: gvgMapping.SrcGlobalVirtualGroupId,
				DstGlobalVirtualGroupId: gvgMapping.DstGlobalVirtualGroupId,
				SecondarySpBlsSignature: gvgMapping.SecondarySpBlsSignature,
			})
		}

		signedTx, err := session.CompleteMigrateBucket(
			migrateBucket.GetBucketName(),
			migrateBucket.GetGlobalVirtualGroupFamilyId(),
			gvgMappings,
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrCompleteMigrateBucketOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrCompleteMigrateBucketOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrCompleteMigrateBucketOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrCompleteMigrateBucketOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast complete migrate bucket tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrCompleteMigrateBucketOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrCompleteMigrateBucketOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrCompleteMigrateBucketOnChain.SetError(fmt.Errorf("failed to broadcast complete migrate bucket, error: %v", broadcastErr))
	return "", ErrCompleteMigrateBucketOnChain
}

func (client *MocaChainSignClient) UpdateSPPrice(ctx context.Context, scope SignType,
	priceInfo *sptypes.MsgUpdateSpStoragePrice,
) (string, error) {
	log.Infow("signer starts to complete update SP price info", "scope", scope)
	if priceInfo == nil {
		log.CtxError(ctx, "complete migrate bucket msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgUpdateStorageSPPrice := &sptypes.MsgUpdateSpStoragePrice{
		SpAddress:     km.GetAddr().String(),
		ReadPrice:     priceInfo.ReadPrice,
		FreeReadQuota: priceInfo.FreeReadQuota,
		StorePrice:    priceInfo.StorePrice,
	}
	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC
	txOpt := &ctypes.TxOption{
		Mode:      &mode,
		GasLimit:  client.gasInfo[UpdateSPPrice].GasLimit,
		FeeAmount: client.gasInfo[UpdateSPPrice].FeeAmount,
	}

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgUpdateStorageSPPrice}, txOpt, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast update sp price msg", "error", err, "update_sp_price",
			msgUpdateStorageSPPrice.String())
		ErrUpdateSPPriceOnChain.SetError(fmt.Errorf("failed to broadcast msg to update sp price, error: %v", err))
		return "", ErrUpdateSPPriceOnChain
	}

	client.operatorAccNonce = nonce + 1
	return txHash, nil
}

func (client *MocaChainSignClient) UpdateSPPriceEvm(ctx context.Context, scope SignType,
	priceInfo *sptypes.MsgUpdateSpStoragePrice,
) (string, error) {
	log.Infow("signer starts to complete update SP price info", "scope", scope)
	if priceInfo == nil {
		log.CtxError(ctx, "complete migrate bucket msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()
	nonce := client.operatorAccNonce

	msgUpdateStorageSPPrice := &sptypes.MsgUpdateSpStoragePrice{
		SpAddress:     km.GetAddr().String(),
		ReadPrice:     priceInfo.ReadPrice,
		FreeReadQuota: priceInfo.FreeReadQuota,
		StorePrice:    priceInfo.StorePrice,
	}
	operation, err := evmOperationFingerprint(msgUpdateStorageSPPrice)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrUpdateSPPriceOnChain.SetError(resolveErr)
			return pendingHash, ErrUpdateSPPriceOnChain
		}
		return pendingHash, nil
	}
	txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[UpdateSPPrice].GasLimit, nonce, client.maxEvmGasPrice)
	if err != nil {
		log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
		return "", err
	}
	txOpts.NoSend = true

	session, err := CreateStorageProviderSession(client.evmClient, *txOpts, evmostypes.SpAddress)
	if err != nil {
		log.CtxErrorw(ctx, "failed to create session", "error", err)
		return "", err
	}

	signedTx, err := session.UpdateSPPrice(
		priceInfo.ReadPrice.BigInt(),
		priceInfo.FreeReadQuota,
		priceInfo.StorePrice.BigInt(),
	)

	if err != nil {
		log.CtxErrorw(ctx, "failed to sign EVM tx", "error", err)
		ErrUpdateSPPriceOnChain.SetError(fmt.Errorf("failed to sign EVM tx: %w", err))
		return "", ErrUpdateSPPriceOnChain
	}
	txHash, err := client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
	if txHash == "" && err != nil {
		if isEvmNonceRejection(err) {
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr := client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get approval account nonce", "error", err)
				ErrUpdateSPPriceOnChain.SetError(fmt.Errorf("failed to get approval account nonce, error: %v", err))
				return "", ErrUpdateSPPriceOnChain
			}
			client.operatorAccNonce = nonce
		}

		log.CtxErrorw(ctx, "failed to broadcast update sp price msg", "error", err, "update_sp_price",
			msgUpdateStorageSPPrice.String())
		ErrUpdateSPPriceOnChain.SetError(fmt.Errorf("failed to broadcast msg to update sp price, error: %v", err))
		return "", ErrUpdateSPPriceOnChain
	}

	// the nonce counts as used once the transaction may have been admitted
	client.operatorAccNonce = nonce + 1

	if err != nil {
		log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
		ErrUpdateSPPriceOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
		return txHash, ErrUpdateSPPriceOnChain
	}

	return txHash, nil
}

func (client *MocaChainSignClient) SwapOut(ctx context.Context, scope SignType,
	swapOut *virtualgrouptypes.MsgSwapOut,
) (string, error) {
	log.Infow("signer starts to swap out", "scope", scope)
	if swapOut == nil {
		log.CtxError(ctx, "failed to swap out due to pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgSwapOut := virtualgrouptypes.NewMsgSwapOut(km.GetAddr(), swapOut.GetGlobalVirtualGroupFamilyId(), swapOut.GetGlobalVirtualGroupIds(),
		swapOut.GetSuccessorSpId())
	msgSwapOut.SuccessorSpApproval = &common.Approval{
		ExpiredHeight: swapOut.SuccessorSpApproval.GetExpiredHeight(),
		Sig:           swapOut.SuccessorSpApproval.GetSig(),
	}
	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC

	txOpt := &ctypes.TxOption{
		Mode:      &mode,
		GasLimit:  client.gasInfo[SwapOut].GasLimit,
		FeeAmount: client.gasInfo[SwapOut].FeeAmount,
	}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgSwapOut}, txOpt, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast swap out", "swap_out", msgSwapOut.String(), "error", err)
		ErrSwapOutOnChain.SetError(fmt.Errorf("failed to broadcast swap out tx, error: %v", err))
		return "", ErrSwapOutOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast start swap out tx", "tx_hash", txHash, "swap_out", msgSwapOut.String())
	return txHash, nil
}

func (client *MocaChainSignClient) SwapOutEvm(ctx context.Context, scope SignType,
	swapOut *virtualgrouptypes.MsgSwapOut,
) (string, error) {
	log.Infow("signer starts to swap out", "scope", scope)
	if swapOut == nil {
		log.CtxError(ctx, "failed to swap out due to pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgSwapOut := virtualgrouptypes.NewMsgSwapOut(km.GetAddr(), swapOut.GetGlobalVirtualGroupFamilyId(), swapOut.GetGlobalVirtualGroupIds(),
		swapOut.GetSuccessorSpId())
	msgSwapOut.SuccessorSpApproval = &common.Approval{
		ExpiredHeight: swapOut.SuccessorSpApproval.GetExpiredHeight(),
		Sig:           swapOut.SuccessorSpApproval.GetSig(),
	}

	operation, err := evmOperationFingerprint(msgSwapOut)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrSwapOutOnChain.SetError(resolveErr)
			return pendingHash, ErrSwapOutOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash       string
		nonce        uint64
		nonceErr     error
		broadcastErr error
	)

	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[SwapOut].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		SuccessorSpApproval := virtualgroup.Approval{
			ExpiredHeight:              msgSwapOut.SuccessorSpApproval.ExpiredHeight,
			GlobalVirtualGroupFamilyId: msgSwapOut.SuccessorSpApproval.GlobalVirtualGroupFamilyId,
			Sig:                        msgSwapOut.SuccessorSpApproval.Sig,
		}
		signedTx, err := session.SwapOut(
			swapOut.GetGlobalVirtualGroupFamilyId(),
			swapOut.GetGlobalVirtualGroupIds(),
			swapOut.GetSuccessorSpId(),
			SuccessorSpApproval,
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrSwapOutOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrSwapOutOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", nonceErr)
				ErrSwapOutOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrSwapOutOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast swap out", "retry_number", i, "swap_out", msgSwapOut.String(), "error", err)
			broadcastErr = err
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrSwapOutOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrSwapOutOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrSwapOutOnChain.SetError(fmt.Errorf("failed to broadcast swap out tx, error: %v", broadcastErr))
	return "", ErrSwapOutOnChain
}

func (client *MocaChainSignClient) CompleteSwapOut(ctx context.Context, scope SignType,
	completeSwapOut *virtualgrouptypes.MsgCompleteSwapOut,
) (string, error) {
	log.Infow("signer starts to complete swap out", "scope", scope)
	if completeSwapOut == nil {
		log.CtxError(ctx, "complete swap out msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCompleteSwapOut := virtualgrouptypes.NewMsgCompleteSwapOut(km.GetAddr(), completeSwapOut.GetGlobalVirtualGroupFamilyId(),
		completeSwapOut.GetGlobalVirtualGroupIds())
	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC

	txOpt := &ctypes.TxOption{
		Mode:      &mode,
		GasLimit:  client.gasInfo[CompleteSwapOut].GasLimit,
		FeeAmount: client.gasInfo[CompleteSwapOut].FeeAmount,
	}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgCompleteSwapOut}, txOpt, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast complete swap out tx", "error", err)
		ErrCompleteSwapOutOnChain.SetError(fmt.Errorf("failed to broadcast complete swap out, error: %v", err))
		return "", ErrCompleteSwapOutOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast complete swap out tx", "tx_hash", txHash, "seal_msg", msgCompleteSwapOut)
	return txHash, nil
}

func (client *MocaChainSignClient) CompleteSwapOutEvm(ctx context.Context, scope SignType,
	completeSwapOut *virtualgrouptypes.MsgCompleteSwapOut,
) (string, error) {
	log.Infow("signer starts to complete swap out", "scope", scope)
	if completeSwapOut == nil {
		log.CtxError(ctx, "complete swap out msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCompleteSwapOut := virtualgrouptypes.NewMsgCompleteSwapOut(km.GetAddr(), completeSwapOut.GetGlobalVirtualGroupFamilyId(),
		completeSwapOut.GetGlobalVirtualGroupIds())

	operation, err := evmOperationFingerprint(msgCompleteSwapOut)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrCompleteSwapOutOnChain.SetError(resolveErr)
			return pendingHash, ErrCompleteSwapOutOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash       string
		nonce        uint64
		nonceErr     error
		broadcastErr error
	)

	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[CompleteSwapOut].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.CompleteSwapOut(
			completeSwapOut.GetGlobalVirtualGroupFamilyId(),
			completeSwapOut.GetGlobalVirtualGroupIds(),
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrCompleteSwapOutOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrCompleteSwapOutOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", nonceErr)
				ErrCompleteSwapOutOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", nonceErr))
				return "", ErrCompleteSwapOutOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast complete swap out tx", "retry_number", i, "error", err)
			broadcastErr = err
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrCompleteSwapOutOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrCompleteSwapOutOnChain
		}

		return txHash, nil
	}

	ErrCompleteSwapOutOnChain.SetError(fmt.Errorf("failed to broadcast complete swap out, error: %v", broadcastErr))
	return "", ErrCompleteSwapOutOnChain
}

func (client *MocaChainSignClient) SPExit(ctx context.Context, scope SignType,
	spExit *virtualgrouptypes.MsgStorageProviderExit,
) (string, error) {
	log.Infow("signer starts to sp exit", "scope", scope)
	if spExit == nil {
		log.CtxError(ctx, "sp exit msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgSPExit := virtualgrouptypes.NewMsgStorageProviderExit(km.GetAddr())

	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC
	txOpt := &ctypes.TxOption{
		Mode:      &mode,
		GasLimit:  client.gasInfo[SPExit].GasLimit,
		FeeAmount: client.gasInfo[SPExit].FeeAmount,
	}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgSPExit}, txOpt, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast start sp exit tx", "error", err)
		ErrSPExitOnChain.SetError(fmt.Errorf("failed to broadcast start sp exit, error: %v", err))
		return "", ErrSPExitOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast start sp exit tx", "tx_hash", txHash, "exit_msg", msgSPExit)
	return txHash, nil
}

func (client *MocaChainSignClient) SPExitEvm(ctx context.Context, scope SignType,
	spExit *virtualgrouptypes.MsgStorageProviderExit,
) (string, error) {
	log.Infow("signer starts to sp exit", "scope", scope)
	if spExit == nil {
		log.CtxError(ctx, "sp exit msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgSPExit := virtualgrouptypes.NewMsgStorageProviderExit(km.GetAddr())

	operation, err := evmOperationFingerprint(msgSPExit)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrSPExitOnChain.SetError(resolveErr)
			return pendingHash, ErrSPExitOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)

	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[SPExit].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.SpExit()

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrSPExitOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrSPExitOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", nonceErr)
				ErrSPExitOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", nonceErr))
				return "", ErrSPExitOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast start sp exit tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrSPExitOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrSPExitOnChain
		}

		return txHash, nil
	}

	ErrSPExitOnChain.SetError(fmt.Errorf("failed to broadcast start sp exit, error: %v", err))
	return "", ErrSPExitOnChain
}

func (client *MocaChainSignClient) CompleteSPExit(ctx context.Context, scope SignType,
	completeSPExit *virtualgrouptypes.MsgCompleteStorageProviderExit,
) (string, error) {
	log.Infow("signer starts to complete sp exit", "scope", scope)
	if completeSPExit == nil {
		log.CtxError(ctx, "complete sp exit msg pointer dangling")
		return "", ErrDanglingPointer
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCompleteSPExit := &virtualgrouptypes.MsgCompleteStorageProviderExit{
		StorageProvider: completeSPExit.StorageProvider,
		Operator:        completeSPExit.Operator,
	}

	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC

	txOpt := &ctypes.TxOption{
		Mode:      &mode,
		GasLimit:  client.gasInfo[CompleteSPExit].GasLimit,
		FeeAmount: client.gasInfo[CompleteSPExit].FeeAmount,
	}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgCompleteSPExit}, txOpt, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast complete sp exit tx", "error", err)
		ErrCompleteSPExitOnChain.SetError(fmt.Errorf("failed to broadcast complete sp exit, error: %v", err))
		return "", ErrCompleteSPExitOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast complete sp exit tx", "tx_hash", txHash, "complete_sp_exit_msg", msgCompleteSPExit)
	return txHash, nil
}

func (client *MocaChainSignClient) CompleteSPExitEvm(ctx context.Context, scope SignType,
	completeSPExit *virtualgrouptypes.MsgCompleteStorageProviderExit,
) (string, error) {
	log.Infow("signer starts to complete sp exit", "scope", scope)
	if completeSPExit == nil {
		log.CtxError(ctx, "complete sp exit msg pointer dangling")
		return "", ErrDanglingPointer
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCompleteSPExit := &virtualgrouptypes.MsgCompleteStorageProviderExit{
		StorageProvider: completeSPExit.StorageProvider,
		Operator:        completeSPExit.Operator,
	}

	operation, err := completeSPExitEvmFingerprint(msgCompleteSPExit)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrCompleteSPExitOnChain.SetError(resolveErr)
			return pendingHash, ErrCompleteSPExitOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[CompleteSPExit].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.CompleteSPExit(
			completeSPExit.Operator,
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrCompleteSPExitOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrCompleteSPExitOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", nonceErr)
				ErrCompleteSPExitOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", nonceErr))
				return "", ErrCompleteSPExitOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast complete sp exit tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrCompleteSPExitOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrCompleteSPExitOnChain
		}

		return txHash, nil
	}

	ErrCompleteSPExitOnChain.SetError(fmt.Errorf("failed to broadcast complete sp exit, error: %v", err))
	return "", ErrCompleteSPExitOnChain
}

func (client *MocaChainSignClient) RejectMigrateBucket(ctx context.Context, scope SignType,
	msg *storagetypes.MsgRejectMigrateBucket,
) (string, error) {
	log.Infow("signer starts to reject migrate bucket", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "reject migrate bucket msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgRejectMigrateBucket := storagetypes.NewMsgRejectMigrateBucket(km.GetAddr(), msg.GetBucketName())

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgRejectMigrateBucket}, &ctypes.TxOption{}, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast reject migrate bucket tx", "error", err)
		ErrRejectMigrateBucketOnChain.SetError(fmt.Errorf("failed to broadcast reject migrate bucket, error: %v", err))
		return "", ErrRejectMigrateBucketOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast reject migrate bucket tx", "tx_hash", txHash, "reject_migrate_bucket_msg", msgRejectMigrateBucket)
	return txHash, nil
}

func (client *MocaChainSignClient) RejectMigrateBucketEvm(ctx context.Context, scope SignType,
	msg *storagetypes.MsgRejectMigrateBucket,
) (string, error) {
	log.Infow("signer starts to reject migrate bucket", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "reject migrate bucket msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgRejectMigrateBucket := storagetypes.NewMsgRejectMigrateBucket(km.GetAddr(), msg.GetBucketName())
	operation, err := evmOperationFingerprint(msgRejectMigrateBucket)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrRejectMigrateBucketOnChain.SetError(resolveErr)
			return pendingHash, ErrRejectMigrateBucketOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[RejectMigrateBucket].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateStorageSession(client.evmClient, *txOpts, evmostypes.StorageAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.RejectMigrateBucket(
			msg.GetBucketName(),
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrRejectMigrateBucketOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrRejectMigrateBucketOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrRejectMigrateBucketOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrRejectMigrateBucketOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast reject migrate bucket tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrRejectMigrateBucketOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrRejectMigrateBucketOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrRejectMigrateBucketOnChain.SetError(fmt.Errorf("failed to broadcast reject migrate bucket, error: %v", err))
	return "", ErrRejectMigrateBucketOnChain
}

func (client *MocaChainSignClient) Deposit(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgDeposit,
) (string, error) {
	log.Infow("signer starts to make deposit into GVG", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "deposit msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgDeposit := virtualgrouptypes.NewMsgDeposit(km.GetAddr(), msg.GlobalVirtualGroupId, msg.Deposit)

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgDeposit}, &ctypes.TxOption{}, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast deposit tx", "error", err)
		ErrDepositOnChain.SetError(fmt.Errorf("failed to broadcast deposit, error: %v", err))
		return "", ErrDepositOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast deposit tx", "tx_hash", txHash, "deposit_msg", msgDeposit)
	return txHash, nil
}

func (client *MocaChainSignClient) DepositEvm(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgDeposit,
) (string, error) {
	log.Infow("signer starts to make deposit into GVG", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "deposit msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgDeposit := virtualgrouptypes.NewMsgDeposit(km.GetAddr(), msg.GlobalVirtualGroupId, msg.Deposit)
	operation, err := evmOperationFingerprint(msgDeposit)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrDepositOnChain.SetError(resolveErr)
			return pendingHash, ErrDepositOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[Deposit].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		deposit := virtualgroup.Coin{
			Denom:  msg.Deposit.Denom,
			Amount: msg.Deposit.Amount.BigInt(),
		}
		signedTx, err := session.Deposit(
			msg.GlobalVirtualGroupId,
			deposit,
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrDepositOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrDepositOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrDepositOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrDepositOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast deposit tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrDepositOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrDepositOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrDepositOnChain.SetError(fmt.Errorf("failed to broadcast deposit, error: %v", err))
	return "", ErrDepositOnChain
}

func (client *MocaChainSignClient) DeleteGlobalVirtualGroup(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgDeleteGlobalVirtualGroup,
) (string, error) {
	log.Infow("signer starts to delete GVG", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "delete GVG msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgDeleteGlobalVirtualGroup := virtualgrouptypes.NewMsgDeleteGlobalVirtualGroup(km.GetAddr(), msg.GetGlobalVirtualGroupId())

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgDeleteGlobalVirtualGroup}, &ctypes.TxOption{}, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast delete GVG tx", "error", err)
		ErrDeleteGVGOnChain.SetError(fmt.Errorf("failed to broadcast delete GVG, error: %v", err))
		return "", ErrDeleteGVGOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast delete GVG tx", "tx_hash", txHash, "reject_migrate_bucket_msg", msgDeleteGlobalVirtualGroup)
	return txHash, nil
}

func (client *MocaChainSignClient) DeleteGlobalVirtualGroupEvm(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgDeleteGlobalVirtualGroup,
) (string, error) {
	log.Infow("signer starts to delete GVG", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "delete GVG msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgDeleteGlobalVirtualGroup := virtualgrouptypes.NewMsgDeleteGlobalVirtualGroup(km.GetAddr(), msg.GetGlobalVirtualGroupId())
	operation, err := evmOperationFingerprint(msgDeleteGlobalVirtualGroup)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrDeleteGVGOnChain.SetError(resolveErr)
			return pendingHash, ErrDeleteGVGOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[DeleteGlobalVirtualGroup].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.DeleteGlobalVirtualGroup(
			msg.GetGlobalVirtualGroupId(),
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrDeleteGVGOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrDeleteGVGOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrDeleteGVGOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrDeleteGVGOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast delete GVG tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrDeleteGVGOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrDeleteGVGOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrDeleteGVGOnChain.SetError(fmt.Errorf("failed to broadcast delete GVG, error: %v", err))
	return "", ErrDeleteGVGOnChain
}

func (client *MocaChainSignClient) DelegateCreateObject(ctx context.Context, scope SignType,
	msg *storagetypes.MsgDelegateCreateObject,
) (string, error) {
	if msg == nil {
		log.CtxError(ctx, "delegate create object msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	client.opLock.Lock()
	defer client.opLock.Unlock()

	msg.Operator = km.GetAddr().String()

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msg}, &ctypes.TxOption{}, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast delegate create object tx", "error", err)
		ErrDelegateCreateObjectOnChain.SetError(fmt.Errorf("failed to delegate create object, error: %v", err))
		return "", ErrDelegateCreateObjectOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast delegate create object tx", "tx_hash", txHash, "delegate_update_object_msg", msg)
	return txHash, nil
}

func (client *MocaChainSignClient) DelegateCreateObjectEvm(ctx context.Context, scope SignType,
	msg *storagetypes.MsgDelegateCreateObject,
) (string, error) {
	if msg == nil {
		log.CtxError(ctx, "delegate create object msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msg.Operator = km.GetAddr().String()
	operation, err := evmOperationFingerprint(msg)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrDelegateCreateObjectOnChain.SetError(resolveErr)
			return pendingHash, ErrDelegateCreateObjectOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[DelegateCreateObject].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateStorageSession(client.evmClient, *txOpts, evmostypes.StorageAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		expectChecksums := make([]string, 0)
		for _, checksum := range msg.ExpectChecksums {
			checksumStr := base64.StdEncoding.EncodeToString(checksum)
			expectChecksums = append(expectChecksums, checksumStr)
		}
		signedTx, err := session.DelegateCreateObject(
			msg.Creator,
			msg.BucketName,
			msg.ObjectName,
			msg.PayloadSize,
			msg.ContentType,
			uint8(msg.Visibility),
			expectChecksums,
			uint8(msg.RedundancyType),
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrDelegateCreateObjectOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrDelegateCreateObjectOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrDelegateCreateObjectOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrDelegateCreateObjectOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast delegate create object tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrDelegateCreateObjectOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrDelegateCreateObjectOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrDelegateCreateObjectOnChain.SetError(fmt.Errorf("failed to delegate create object, error: %v", err))
	return "", ErrDelegateCreateObjectOnChain
}

func (client *MocaChainSignClient) DelegateUpdateObjectContent(ctx context.Context, scope SignType,
	msg *storagetypes.MsgDelegateUpdateObjectContent,
) (string, error) {
	if msg == nil {
		log.CtxError(ctx, "delegate update object content msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	client.opLock.Lock()
	defer client.opLock.Unlock()

	msg.Operator = km.GetAddr().String()

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msg}, &ctypes.TxOption{}, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast delegate update object content tx", "error", err)
		ErrDelegateUpdateObjectContentOnChain.SetError(fmt.Errorf("failed to broadcast delegte update object, error: %v", err))
		return "", ErrDelegateUpdateObjectContentOnChain
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast delegate update object content tx", "tx_hash", txHash, "delegate_update_object_content_msg", msg)
	return txHash, nil
}

func (client *MocaChainSignClient) DelegateUpdateObjectContentEvm(ctx context.Context, scope SignType,
	msg *storagetypes.MsgDelegateUpdateObjectContent,
) (string, error) {
	if msg == nil {
		log.CtxError(ctx, "delegate update object content msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msg.Operator = km.GetAddr().String()
	operation, err := evmOperationFingerprint(msg)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrDelegateUpdateObjectContentOnChain.SetError(resolveErr)
			return pendingHash, ErrDelegateUpdateObjectContentOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[DelegateUpdateObjectContent].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateStorageSession(client.evmClient, *txOpts, evmostypes.StorageAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		expectChecksums := make([]string, 0)
		for _, checksum := range msg.ExpectChecksums {
			checksumStr := base64.StdEncoding.EncodeToString(checksum)
			expectChecksums = append(expectChecksums, checksumStr)
		}
		signedTx, err := session.DelegateUpdateObjectContent(
			msg.Updater,
			msg.BucketName,
			msg.ObjectName,
			msg.PayloadSize,
			msg.ContentType,
			expectChecksums,
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrDelegateUpdateObjectContentOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrDelegateUpdateObjectContentOnChain
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrDelegateUpdateObjectContentOnChain.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrDelegateUpdateObjectContentOnChain
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast delegate update object content tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrDelegateUpdateObjectContentOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrDelegateUpdateObjectContentOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrDelegateUpdateObjectContentOnChain.SetError(fmt.Errorf("failed to broadcast delegte update object, error: %v", err))
	return "", ErrDelegateUpdateObjectContentOnChain
}

func containsAnyReason(msg string, reasons []string) bool {
	for _, reason := range reasons {
		if strings.Contains(msg, reason) {
			return true
		}
	}
	return false
}

// isEvmNonceLowError and isEvmNonceHighError match a node's refusal of a
// transaction whose nonce differs from the account nonce. On a Moca node
// (no experimental EVM mempool) CheckTx returns these before admitting the
// bytes, so the refused transaction was not admitted.
func isEvmNonceLowError(msg string) bool {
	return containsAnyReason(msg, []string{errMsgNonceTooLow, errMsgInvalidNonce, errMsgNonceLowerThanAccount})
}

func isEvmNonceHighError(msg string) bool {
	return containsAnyReason(msg, []string{errMsgNonceTooHigh, errMsgNonceHigherThanAccount})
}

type evmSendOutcome int

const (
	// evmSendAccepted means the node accepted the request. It does not prove
	// the transaction entered the mempool; only a receipt does.
	evmSendAccepted evmSendOutcome = iota
	evmSendAmbiguous
	evmSendNonceRejected
	evmSendRejected
)

// classifyEvmSendError decides whether a failed eth_sendRawTransaction proves
// that the node did not admit the transaction. Only a JSON-RPC error object
// whose message names a verified refusal reason counts; everything else may
// have been admitted.
func classifyEvmSendError(ctx context.Context, err error) evmSendOutcome {
	if err == nil {
		return evmSendAccepted
	}
	// Checked before any message matching: an HTTP or transport failure can
	// carry an arbitrary body (a 503 page may contain "invalid nonce") and
	// says nothing about whether the node already forwarded the transaction.
	if ctx.Err() != nil || stderrors.Is(err, context.Canceled) || stderrors.Is(err, context.DeadlineExceeded) {
		return evmSendAmbiguous
	}
	var httpErr rpc.HTTPError
	if stderrors.As(err, &httpErr) {
		return evmSendAmbiguous
	}
	var rpcErr rpc.Error
	if !stderrors.As(err, &rpcErr) {
		return evmSendAmbiguous
	}
	switch rpcErr.ErrorCode() {
	case evmRPCCodeTimeout, evmRPCCodeInternal:
		// go-ethereum answers -32002 while the handler may still be running.
		return evmSendAmbiguous
	case evmRPCCodeInvalidParams:
		return evmSendRejected
	}
	msg := rpcErr.Error()
	switch {
	case isEvmNonceLowError(msg), isEvmNonceHighError(msg):
		return evmSendNonceRejected
	case containsAnyReason(msg, evmNotAdmittedReasons):
		return evmSendRejected
	}
	return evmSendAmbiguous
}

// evmNodeAnswered reports whether a re-broadcast got a definite answer from
// the node, so sending the same bytes again would not tell us anything new.
func evmNodeAnswered(ctx context.Context, err error) bool {
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	var httpErr rpc.HTTPError
	if stderrors.As(err, &httpErr) {
		return false
	}
	var rpcErr rpc.Error
	if !stderrors.As(err, &rpcErr) {
		return false
	}
	code := rpcErr.ErrorCode()
	return code != evmRPCCodeTimeout && code != evmRPCCodeInternal
}

// evmNonceRejectedError marks a submit the node refused for a nonce mismatch
// before admitting it. Only this error lets a caller refresh the nonce and
// sign the operation again.
type evmNonceRejectedError struct {
	err error
}

func (e *evmNonceRejectedError) Error() string { return e.err.Error() }

func (e *evmNonceRejectedError) Unwrap() error { return e.err }

func isEvmNonceRejection(err error) bool {
	var target *evmNonceRejectedError
	return stderrors.As(err, &target)
}

func (client *MocaChainSignClient) getNonceOnChain(ctx context.Context, gnfdClient *client.MocaClient) (uint64, error) {
	return getNonceOnChainFn(ctx, client, gnfdClient)
}

func waitNextBlockAndGetNonce(ctx context.Context, signClient *MocaChainSignClient, gnfdClient *client.MocaClient) (uint64, error) {
	err := signClient.signer.baseApp.Consensus().WaitForNextBlock(ctx)
	if err != nil {
		log.CtxErrorw(ctx, "failed to wait next block", "error", err)
		return 0, err
	}
	nonce, err := gnfdClient.GetNonce(ctx)
	if err != nil {
		log.CtxErrorw(ctx, "failed to get nonce on chain", "error", err)
		return 0, err
	}
	return nonce, nil
}

func (client *MocaChainSignClient) broadcastTxWithSequenceRetry(ctx context.Context, gnfdClient *client.MocaClient,
	msgs []sdk.Msg, txOpt *ctypes.TxOption, nonceCache *uint64, opts ...grpc.CallOption,
) (string, uint64, error) {
	var err error
	for attempt := 0; attempt < BroadcastTxRetry; attempt++ {
		nonce := *nonceCache
		txOpt.Nonce = nonce
		hash, broadcastErr := client.broadcastTxOnce(ctx, gnfdClient, msgs, txOpt, opts...)
		if broadcastErr == nil {
			return hash, nonce, nil
		}
		err = broadcastErr
		if !errors.IsOf(err, sdkErrors.ErrWrongSequence) || attempt == BroadcastTxRetry-1 {
			return "", nonce, err
		}
		refreshed, refreshErr := getCosmosNonceFn(gnfdClient, ctx)
		if refreshErr != nil {
			return "", nonce, errors.Wrap(refreshErr, "failed to get nonce on chain")
		}
		*nonceCache = refreshed
	}
	return "", *nonceCache, err
}

func (client *MocaChainSignClient) broadcastTx(ctx context.Context, gnfdClient *client.MocaClient,
	msgs []sdk.Msg, txOpt *ctypes.TxOption, opts ...grpc.CallOption,
) (string, uint64, error) {
	nonce := txOpt.Nonce
	hash, err := client.broadcastTxOnce(ctx, gnfdClient, msgs, txOpt, opts...)
	return hash, nonce, err
}

func (client *MocaChainSignClient) broadcastTxOnce(ctx context.Context, gnfdClient *client.MocaClient,
	msgs []sdk.Msg, txOpt *ctypes.TxOption, opts ...grpc.CallOption,
) (string, error) {
	resp, err := broadcastCosmosTxFn(gnfdClient, ctx, msgs, txOpt, opts...)
	if err != nil {
		if strings.Contains(err.Error(), "account sequence mismatch") {
			return "", sdkErrors.ErrWrongSequence
		}
		return "", errors.Wrap(err, "failed to broadcast tx with moca client")
	}
	if resp.TxResponse.Code == sdkErrors.ErrWrongSequence.ABCICode() {
		return "", sdkErrors.ErrWrongSequence
	}
	if resp.TxResponse.Code != 0 {
		return "", fmt.Errorf("failed to broadcast tx, resp code: %d, code space: %s", resp.TxResponse.Code, resp.TxResponse.Codespace)
	}
	return resp.TxResponse.TxHash, nil
}

func (svc *MocaChainSignClient) waitForTransactionReceipt(ctx context.Context, txHash ethcmn.Hash) (*types.Receipt, error) {
	// Keep original 5-minute upper bound while switching to block-driven waiting.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Use any connected client to wait for next block; operator is sufficient and non-intrusive.
	gnfdCli := svc.mocaClients[SignOperator]
	receipt, err := waitForEvmTxFn(waitCtx, svc.evmClient, gnfdCli, txHash)
	if err != nil {
		// Preserve prior error semantics/messages as much as possible.
		if waitCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("timeout waiting for transaction receipt: hash=%s", txHash.Hex())
		}
		return nil, fmt.Errorf("failed to get transaction receipt: %w", err)
	}
	return receipt, nil
}

func evmOperationFingerprint(msg sdk.Msg) (ethcmn.Hash, error) {
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		return ethcmn.Hash{}, fmt.Errorf("failed to marshal EVM operation: %w", err)
	}
	return crypto.Keccak256Hash([]byte(fmt.Sprintf("%T\x00", msg)), msgBytes), nil
}

func completeSPExitEvmFingerprint(msg *virtualgrouptypes.MsgCompleteStorageProviderExit) (ethcmn.Hash, error) {
	return evmOperationFingerprint(&virtualgrouptypes.MsgCompleteStorageProviderExit{
		Operator: msg.Operator,
	})
}

// submitEvmTx sends a transaction signed with NoSend and waits for its
// receipt. The evidence and the pending row are committed to the state store
// before the first send, so an unknown outcome blocks the scope, across
// restarts too, instead of letting the operation be signed again at a later
// nonce.
//
// An empty hash means nothing may have been admitted: the context was done
// or the state could not be recorded before sending, or the node verifiably
// refused the bytes. A nonce refusal is returned as evmNonceRejectedError
// only once the refusal is recorded; it is the only case where the caller may
// refresh the nonce and sign again. A non-empty hash with an error means the
// transaction may be on chain and stays pending.
func (client *MocaChainSignClient) submitEvmTx(ctx context.Context, scope SignType, operation ethcmn.Hash,
	nonce uint64, signedTx *types.Transaction,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	st := client.evm
	if st == nil {
		return "", errEvmStateNotConfigured
	}
	fence, ok := st.lease.fence()
	if !ok {
		return "", errEvmLeaseNotHeld
	}
	raw, err := signedTx.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("failed to encode EVM transaction: %w", err)
	}
	txHash := signedTx.Hash()

	pending, err := client.recordPendingEvmTx(ctx, st, fence, scope, operation, signedTx, raw)
	if err != nil {
		return "", err
	}
	sendErr := sendEvmTxFn(ctx, client.evmClient, signedTx)
	switch classifyEvmSendError(ctx, sendErr) {
	case evmSendNonceRejected:
		kind := terminalNotAdmittedNonceLow
		if isEvmNonceHighError(sendErr.Error()) {
			kind = terminalNotAdmittedNonceHigh
		}
		if err := client.finishPendingEvmTx(ctx, st, fence, pending, pendingTerminal{Kind: kind, Detail: sendErr.Error()}); err != nil {
			// Not evmNonceRejectedError: the caller must not re-sign while
			// the refused bytes are still recorded as pending.
			return "", fmt.Errorf("EVM tx refused for its nonce (%v), but the refusal could not be recorded: %w", sendErr, err)
		}
		return "", &evmNonceRejectedError{err: sendErr}
	case evmSendRejected:
		if err := client.finishPendingEvmTx(ctx, st, fence, pending,
			pendingTerminal{Kind: terminalNotAdmittedRejected, Detail: sendErr.Error()}); err != nil {
			return "", fmt.Errorf("EVM tx refused by node (%v), but the refusal could not be recorded: %w", sendErr, err)
		}
		return "", sendErr
	case evmSendAmbiguous:
		client.markPendingEvmTxAmbiguous(ctx, st, fence, pending)
		log.CtxWarnw(ctx, "EVM tx submit outcome unknown; re-broadcasting the same signed bytes",
			"scope", scope, "nonce", nonce, "tx_hash", txHash.Hex(), "error", sendErr)
		// No response from here on may clear the entry: a refusal of a
		// re-send can mean the first send was admitted.
		for attempt := 1; attempt < BroadcastTxRetry && ctx.Err() == nil; attempt++ {
			resendCtx, cancel := context.WithTimeout(ctx, evmResendTimeout)
			resendErr := sendEvmTxFn(resendCtx, client.evmClient, signedTx)
			cancel()
			log.CtxWarnw(ctx, "re-broadcast EVM tx", "tx_hash", txHash.Hex(), "attempt", attempt, "error", resendErr)
			if evmNodeAnswered(ctx, resendErr) {
				break
			}
		}
	}

	receipt, err := client.waitForTransactionReceipt(ctx, txHash)
	if err != nil {
		return txHash.String(), err
	}
	terminal := pendingTerminal{Kind: terminalSuccessConsume, ReceiptStatus: uint64Ptr(receipt.Status), Source: evmOutcomeSourceSubmit}
	if receipt.BlockNumber != nil {
		terminal.ReceiptBlock = receipt.BlockNumber.Uint64()
	}
	if err := client.finishPendingEvmTx(ctx, st, fence, pending, terminal); err != nil {
		return txHash.String(), fmt.Errorf("EVM tx confirmed but its state could not be recorded: %w", err)
	}
	log.CtxDebugw(ctx, "transaction confirmed", "tx_hash", txHash.Hex(), "block", receipt.BlockNumber.Uint64())
	return txHash.String(), nil
}

func (client *MocaChainSignClient) ReserveSwapIn(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgReserveSwapIn,
) (string, error) {
	log.Infow("signer starts to reserve swap in", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "reserve swap in msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgReserveSwapIn := virtualgrouptypes.NewMsgReserveSwapIn(km.GetAddr(), msg.GetTargetSpId(), msg.GetGlobalVirtualGroupFamilyId(), msg.GetGlobalVirtualGroupId())

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgReserveSwapIn}, &ctypes.TxOption{}, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast reserve swap in tx", "error", err)
		ErrReserveSwapIn.SetError(fmt.Errorf("failed to broadcast reserve swap in, error: %v", err))
		return "", ErrReserveSwapIn
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast reserve swap in tx", "tx_hash", txHash, "reserve_swap_in_msg", msgReserveSwapIn)
	return txHash, nil
}

func (client *MocaChainSignClient) ReserveSwapInEvm(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgReserveSwapIn,
) (string, error) {
	log.Infow("signer starts to reserve swap in", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "reserve swap in msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgReserveSwapIn := virtualgrouptypes.NewMsgReserveSwapIn(km.GetAddr(), msg.GetTargetSpId(), msg.GetGlobalVirtualGroupFamilyId(), msg.GetGlobalVirtualGroupId())
	operation, err := evmOperationFingerprint(msgReserveSwapIn)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrReserveSwapIn.SetError(resolveErr)
			return pendingHash, ErrReserveSwapIn
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[ReserveSwapIn].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.ReserveSwapIn(
			msg.GetTargetSpId(),
			msg.GetGlobalVirtualGroupFamilyId(),
			msg.GetGlobalVirtualGroupId(),
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrReserveSwapIn.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrReserveSwapIn
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrReserveSwapIn.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrReserveSwapIn
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast reserve swap in tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrReserveSwapIn.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrReserveSwapIn
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrReserveSwapIn.SetError(fmt.Errorf("failed to broadcast reserve swap in, error: %v", err))
	return "", ErrReserveSwapIn
}

func (client *MocaChainSignClient) CompleteSwapIn(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgCompleteSwapIn,
) (string, error) {
	log.Infow("signer starts to complete swap in", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "complete swap in msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCompleteSwapIn := virtualgrouptypes.NewMsgCompleteSwapIn(km.GetAddr(), msg.GetGlobalVirtualGroupFamilyId(), msg.GetGlobalVirtualGroupId())

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgCompleteSwapIn}, &ctypes.TxOption{}, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast complete swap in tx", "error", err)
		ErrCompleteSwapIn.SetError(fmt.Errorf("failed to broadcast rcomplete swap in, error: %v", err))
		return "", ErrCompleteSwapIn
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast complete swap in tx", "tx_hash", txHash, "complete_swap_in_msg", msgCompleteSwapIn)
	return txHash, nil
}

func (client *MocaChainSignClient) CompleteSwapInEvm(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgCompleteSwapIn,
) (string, error) {
	log.Infow("signer starts to complete swap in", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "complete swap in msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCompleteSwapIn := virtualgrouptypes.NewMsgCompleteSwapIn(km.GetAddr(), msg.GetGlobalVirtualGroupFamilyId(), msg.GetGlobalVirtualGroupId())
	operation, err := evmOperationFingerprint(msgCompleteSwapIn)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrCompleteSwapIn.SetError(resolveErr)
			return pendingHash, ErrCompleteSwapIn
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[CompleteSwapIn].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.CompleteSwapIn(
			msg.GetGlobalVirtualGroupFamilyId(),
			msg.GetGlobalVirtualGroupId(),
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrCompleteSwapIn.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrCompleteSwapIn
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrCompleteSwapIn.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrCompleteSwapIn
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast complete swap in tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrCompleteSwapIn.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrCompleteSwapIn
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrCompleteSwapIn.SetError(fmt.Errorf("failed to broadcast rcomplete swap in, error: %v", err))
	return "", ErrCompleteSwapIn
}

func (client *MocaChainSignClient) CancelSwapIn(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgCancelSwapIn,
) (string, error) {
	log.Infow("signer starts to cancel swap in", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "cancel swap in msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCancelSwapIn := virtualgrouptypes.NewMsgCancelSwapIn(km.GetAddr(), msg.GetGlobalVirtualGroupFamilyId(), msg.GetGlobalVirtualGroupId())

	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgCancelSwapIn}, &ctypes.TxOption{}, &client.operatorAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast cancel swap in tx", "error", err)
		ErrCancelSwapIn.SetError(fmt.Errorf("failed to broadcast cancel swap in, error: %v", err))
		return "", ErrCancelSwapIn
	}
	client.operatorAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast cancel swap in tx", "tx_hash", txHash, "cancel_swap_in_msg", msgCancelSwapIn)
	return txHash, nil
}

func (client *MocaChainSignClient) CancelSwapInEvm(ctx context.Context, scope SignType,
	msg *virtualgrouptypes.MsgCancelSwapIn,
) (string, error) {
	log.Infow("signer starts to cancel swap in", "scope", scope)
	if msg == nil {
		log.CtxError(ctx, "cancel swap in msg pointer dangling")
		return "", ErrDanglingPointer
	}
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.opLock.Lock()
	defer client.opLock.Unlock()

	msgCancelSwapIn := virtualgrouptypes.NewMsgCancelSwapIn(km.GetAddr(), msg.GetGlobalVirtualGroupFamilyId(), msg.GetGlobalVirtualGroupId())
	operation, err := evmOperationFingerprint(msgCancelSwapIn)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrCancelSwapIn.SetError(resolveErr)
			return pendingHash, ErrCancelSwapIn
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.operatorAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[CancelSwapIn].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateVirtualGroupSession(client.evmClient, *txOpts, evmostypes.VirtualGroupAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}

		signedTx, err := session.CancelSwapIn(
			msg.GetGlobalVirtualGroupFamilyId(),
			msg.GetGlobalVirtualGroupId(),
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrCancelSwapIn.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrCancelSwapIn
			}
			// if nonce mismatches, waiting for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get operator account nonce", "error", err)
				ErrCancelSwapIn.SetError(fmt.Errorf("failed to get operator account nonce, error: %v", err))
				return "", ErrCancelSwapIn
			}
			client.operatorAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast cancel swap in tx", "retry_number", i, "error", err)
			continue
		}
		client.operatorAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrCancelSwapIn.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrCancelSwapIn
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrCancelSwapIn.SetError(fmt.Errorf("failed to broadcast cancel swap in, error: %v", err))
	return "", ErrCancelSwapIn
}

// SealObjectV2 seal the object on the moca chain.
func (client *MocaChainSignClient) SealObjectV2(ctx context.Context, scope SignType,
	sealObject *storagetypes.MsgSealObjectV2,
) (string, error) {
	if sealObject == nil {
		log.CtxError(ctx, "failed to seal object due to pointer dangling")
		return "", ErrDanglingPointer
	}
	ctx = log.WithValue(ctx, log.CtxKeyBucketName, sealObject.GetBucketName())
	ctx = log.WithValue(ctx, log.CtxKeyObjectName, sealObject.GetObjectName())
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}

	client.sealLock.Lock()
	defer client.sealLock.Unlock()

	msgSealObject := storagetypes.NewMsgSealObjectV2(km.GetAddr(),
		sealObject.GetBucketName(), sealObject.GetObjectName(), sealObject.GetGlobalVirtualGroupId(),
		sealObject.GetSecondarySpBlsAggSignatures(), sealObject.GetExpectChecksums())

	mode := tx.BroadcastMode_BROADCAST_MODE_SYNC

	txOpt := &ctypes.TxOption{
		NoSimulate: false,
		Mode:       &mode,
		GasLimit:   client.gasInfo[Seal].GasLimit,
		FeeAmount:  client.gasInfo[Seal].FeeAmount,
	}
	txHash, nonce, err := client.broadcastTxWithSequenceRetry(
		ctx, client.mocaClients[scope], []sdk.Msg{msgSealObject}, txOpt, &client.sealAccNonce,
	)
	if err != nil {
		log.CtxErrorw(ctx, "failed to broadcast seal object tx", "error", err)
		ErrSealObjectOnChain.SetError(fmt.Errorf("failed to broadcast seal object tx, error: %v", err))
		return "", ErrSealObjectOnChain
	}
	client.sealAccNonce = nonce + 1
	log.CtxDebugw(ctx, "succeed to broadcast seal object tx", "tx_hash", txHash, "seal_msg", msgSealObject)
	return txHash, nil
}

func (client *MocaChainSignClient) SealObjectV2Evm(ctx context.Context, scope SignType,
	sealObject *storagetypes.MsgSealObjectV2,
) (string, error) {
	if sealObject == nil {
		log.CtxError(ctx, "failed to seal object due to pointer dangling")
		return "", ErrDanglingPointer
	}
	ctx = log.WithValue(ctx, log.CtxKeyBucketName, sealObject.GetBucketName())
	ctx = log.WithValue(ctx, log.CtxKeyObjectName, sealObject.GetObjectName())
	km, err := client.mocaClients[scope].GetKeyManager()
	if err != nil {
		log.CtxErrorw(ctx, "failed to get private key", "error", err)
		return "", ErrSignMsg
	}
	cosmosChainId, err := client.mocaClients[scope].GetChainID()
	if err != nil {
		return "", err
	}

	chainId, err := evmostypes.ParseChainID(cosmosChainId)
	if err != nil {
		return "", err
	}

	client.sealLock.Lock()
	defer client.sealLock.Unlock()

	msgSealObject := storagetypes.NewMsgSealObjectV2(km.GetAddr(),
		sealObject.GetBucketName(), sealObject.GetObjectName(), sealObject.GetGlobalVirtualGroupId(),
		sealObject.GetSecondarySpBlsAggSignatures(), sealObject.GetExpectChecksums())
	operation, err := evmOperationFingerprint(msgSealObject)
	if err != nil {
		return "", err
	}
	if pendingHash, handled, resolveErr := client.resolvePendingEvmTx(ctx, scope, operation); handled {
		if resolveErr != nil {
			ErrSealObjectOnChain.SetError(resolveErr)
			return pendingHash, ErrSealObjectOnChain
		}
		return pendingHash, nil
	}

	var (
		txHash   string
		nonce    uint64
		nonceErr error
	)
	for i := 0; i < BroadcastTxRetry; i++ {
		nonce = client.sealAccNonce
		txOpts, err := CreateTxOpts(ctx, client.evmClient, client.evmPrivateKeys[scope], chainId, client.gasInfo[Seal].GasLimit, nonce, client.maxEvmGasPrice)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create tx opts", "error", err)
			return "", err
		}
		txOpts.NoSend = true

		session, err := CreateStorageSession(client.evmClient, *txOpts, evmostypes.StorageAddress)
		if err != nil {
			log.CtxErrorw(ctx, "failed to create session", "error", err)
			return "", err
		}
		expectChecksums := make([]string, 0)
		for _, checksum := range msgSealObject.ExpectChecksums {
			checksumStr := base64.StdEncoding.EncodeToString(checksum)
			expectChecksums = append(expectChecksums, checksumStr)
		}

		signedTx, err := session.SealObjectV2(
			sealObject.GetBucketName(),
			sealObject.GetObjectName(),
			sealObject.GetGlobalVirtualGroupId(),
			base64.StdEncoding.EncodeToString(sealObject.GetSecondarySpBlsAggSignatures()),
			expectChecksums,
		)

		if err != nil {
			log.CtxErrorw(ctx, "failed to sign EVM tx", "retry_number", i, "error", err)
			continue
		}
		txHash, err = client.submitEvmTx(ctx, scope, operation, nonce, signedTx)
		if txHash == "" && err != nil {
			if !isEvmNonceRejection(err) {
				log.CtxErrorw(ctx, "EVM tx rejected by node", "error", err)
				ErrSealObjectOnChain.SetError(fmt.Errorf("EVM tx rejected by node: %w", err))
				return "", ErrSealObjectOnChain
			}
			// if nonce mismatch, wait for next block, reset nonce by querying the nonce on chain
			nonce, nonceErr = client.getNonceOnChain(ctx, client.mocaClients[scope])
			if nonceErr != nil {
				log.CtxErrorw(ctx, "failed to get seal account nonce", "error", nonceErr)
				ErrSealObjectOnChain.SetError(fmt.Errorf("failed to get seal account nonce, error: %v", nonceErr))
				return "", ErrSealObjectOnChain
			}
			client.sealAccNonce = nonce

			log.CtxErrorw(ctx, "failed to broadcast seal object tx", "retry_number", i, "error", err)
			continue
		}
		client.sealAccNonce = nonce + 1

		if err != nil {
			log.CtxErrorw(ctx, "EVM tx submitted but unconfirmed", "tx_hash", txHash, "error", err)
			ErrSealObjectOnChain.SetError(fmt.Errorf("transaction failed: %w", err))
			return txHash, ErrSealObjectOnChain
		}

		return txHash, nil
	}

	// failed to broadcast tx
	ErrSealObjectOnChain.SetError(fmt.Errorf("failed to broadcast seal object tx, error: %v", err))
	return "", ErrSealObjectOnChain
}
