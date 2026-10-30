---
title: Signer
---

# Signer

Signer uses the SP's private keys to sign the message, the messages to form a transaction and sign the transaction to broadcast it to Moca BlockChain, or the messages exchanged between SPs.

Signer is an abstract interface to handle the signature of SP and on moca chain operator. It holds all private keys of one SP. Considering the SP account's sequence number, it must be a singleton.

```go
type Signer interface {
    Modular
    // SignCreateBucketApproval signs the MsgCreateBucket for asking create bucket approval.
    SignCreateBucketApproval(ctx context.Context, bucket *storagetypes.MsgCreateBucket) ([]byte, error)
    // SignMigrateBucketApproval signs the MsgMigrateBucket for asking migrate bucket approval
    SignMigrateBucketApproval(ctx context.Context, bucket *storagetypes.MsgMigrateBucket) ([]byte, error)
    // SignCreateObjectApproval signs the MsgCreateObject for asking create object approval.
    SignCreateObjectApproval(ctx context.Context, task *storagetypes.MsgCreateObject) ([]byte, error)
    // SignReplicatePieceApproval signs the ApprovalReplicatePieceTask for asking replicate pieces to secondary SPs.
    SignReplicatePieceApproval(ctx context.Context, task task.ApprovalReplicatePieceTask) ([]byte, error)
    // SignReceivePieceTask signs the ReceivePieceTask for replicating pieces data between SPs.
    SignReceivePieceTask(ctx context.Context, task task.ReceivePieceTask) ([]byte, error)
    // SignSecondarySealBls signs the secondary bls for sealing object.
    SignSecondarySealBls(ctx context.Context, objectID uint64, gvgId uint32, hash [][]byte) ([]byte, error)
    // SignRecoveryPieceTask signs the RecoveryPieceTask for recovering piece data
    SignRecoveryPieceTask(ctx context.Context, task task.RecoveryPieceTask) ([]byte, error)
    // SignP2PPingMsg signs the ping msg for p2p node probing.
    SignP2PPingMsg(ctx context.Context, ping *gfspp2p.GfSpPing) ([]byte, error)
    // SignP2PPongMsg signs the pong msg for p2p to response ping msg.
    SignP2PPongMsg(ctx context.Context, pong *gfspp2p.GfSpPong) ([]byte, error)
    // SealObject signs the MsgSealObject and broadcast the tx to moca.
    SealObject(ctx context.Context, object *storagetypes.MsgSealObject) (string, error)
    // RejectUnSealObject signs the MsgRejectSealObject and broadcast the tx to moca.
    RejectUnSealObject(ctx context.Context, object *storagetypes.MsgRejectSealObject) (string, error)
    // DiscontinueBucket signs the MsgDiscontinueBucket and broadcast the tx to moca.
    DiscontinueBucket(ctx context.Context, bucket *storagetypes.MsgDiscontinueBucket) (string, error)
    // CreateGlobalVirtualGroup signs the MsgCreateGlobalVirtualGroup and broadcast the tx to moca.
    CreateGlobalVirtualGroup(ctx context.Context, gvg *virtualgrouptypes.MsgCreateGlobalVirtualGroup) error
    // SignMigratePiece signs the GfSpMigratePieceTask for migrating piece
    SignMigratePiece(ctx context.Context, task *gfsptask.GfSpMigratePieceTask) ([]byte, error)
    // CompleteMigrateBucket signs the MsgCompleteMigrateBucket and broadcast the tx to moca.
    CompleteMigrateBucket(ctx context.Context, migrateBucket *storagetypes.MsgCompleteMigrateBucket) (string, error)
    // SignSecondarySPMigrationBucket signs secondary sp bls for bucket migration
    SignSecondarySPMigrationBucket(ctx context.Context, signDoc *storagetypes.SecondarySpMigrationBucketSignDoc) ([]byte, error)
    // SwapOut signs the MsgSwapOut and broadcast the tx to moca.
    SwapOut(ctx context.Context, swapOut *virtualgrouptypes.MsgSwapOut) (string, error)
    // SignSwapOut signs the MsgSwapOut for asking swap out approval.
    SignSwapOut(ctx context.Context, swapOut *virtualgrouptypes.MsgSwapOut) ([]byte, error)
    // CompleteSwapOut signs the MsgCompleteSwapOut and broadcast the tx to moca.
    CompleteSwapOut(ctx context.Context, completeSwapOut *virtualgrouptypes.MsgCompleteSwapOut) (string, error)
    // SPExit signs the MsgStorageProviderExit and broadcast the tx to moca.
    SPExit(ctx context.Context, spExit *virtualgrouptypes.MsgStorageProviderExit) (string, error)
    // CompleteSPExit signs the MsgCompleteStorageProviderExit and broadcast the tx to moca.
    CompleteSPExit(ctx context.Context, completeSPExit *virtualgrouptypes.MsgCompleteStorageProviderExit) (string, error)
    // UpdateSPPrice signs the MsgUpdateSpStoragePrice and  broadcast the tx to moca.
    UpdateSPPrice(ctx context.Context, price *sptypes.MsgUpdateSpStoragePrice) (string, error)
}
```

Signer interface inherits [Modular interface](./common/lifecycle_modular.md#modular-interface), so Signer module can be managed by lifecycle and resource manager.

In terms of the functions provided by Signer module, there are ten methods. You can rewrite these methods to meet your own requirements.

## Gas of EVM transactions

Every EVM transaction the signer sends carries an estimated gas limit, not a fixed one.
Before signing, the signer asks the node to estimate the exact call
(`eth_estimateGas`) and adds `[Chain] EvmGasMarginPercent` (default 25, at most 100).

- The per-type `[Chain] *GasLimit` settings (`SealGasLimit`, `CreateGlobalVirtualGroupGasLimit`
  and so on) are upper bounds. A transaction whose estimate is above its bound is not sent,
  and the error names both numbers; the margin never raises a limit above its bound. An unset
  bound takes the default, about twice the largest gas measured on a moca v2 network
  (a seal uses 432k to 473k gas).
- A call the node cannot estimate, for example because it would revert, is not signed or
  sent, and the request fails with the node's reason. The fee for a reverted transaction is
  not spent, and no pending transaction is recorded for it.
- The margin is cheap: the fee market charges the larger of the gas used and half the gas
  limit, so a margin up to 100% is not charged.


## SignReplicatePieceApproval

The second params of SignReplicatePieceApproval is a task interface, the corresponding interface definition is shown below:

- [ApprovalReplicatePieceTask](./common/task.md#approvalreplicatepiecetask)

The corresponding `protobuf` definition is shown below:

- [GfSpReplicatePieceApprovalTask](./common/proto.md#gfspreplicatepieceapprovaltask-proto)

## SignReceivePieceTask

The second params of SignReceivePieceTask is a task interface, the corresponding interface definition is shown below:

- [ReceivePieceTask](./common/task.md#approvalreplicatepiecetask)

The corresponding `protobuf` definition is shown below:

- [GfSpReceivePieceTask](./common/proto.md#gfspreceivepiecetask-proto)

## SignP2PPingMsg

The corresponding `protobuf` definition is shown below:

- [GfSpPing](./common/proto.md#gfspping-proto)

## SignP2PPongMsg

The corresponding `protobuf` definition is shown below:

- [GfSpPong](./common/proto.md#gfsppong-proto)

## SealObject

The corresponding `protobuf` definition is shown below:

- [MsgSealObject](./common/proto.md#msgsealobject)

## RejectUnSealObject

The corresponding `protobuf` definition is shown below:

- [MsgRejectSealObject](./common/proto.md#msgrejectsealobject-proto)

## DiscontinueBucket

The corresponding `protobuf` definition is shown below:

- [MsgDiscontinueBucket](./common/proto.md#msgdiscontinuebucket)

## CreateGlobalVirtualGroup

The corresponding `protobuf` definition is shown below:

- [MsgCreateGlobalVirtualGroup](./common/proto.md#msgcreateglobalvirtualgroup)

## CompleteMigrateBucket

The corresponding `protobuf` definition is shown below:

- [MsgCompleteMigrateBucket](./common/proto.md#msgcompletemigratebucket)

## UpdateSPPrice

The corresponding `protobuf` definition is shown below:

- [MsgUpdateSpStoragePrice](./common/proto.md#msgupdatespstorageprice)

## SwapOut

The corresponding `protobuf` definition is shown below:

- [MsgSwapOut](./common/proto.md#msgswapout)

## CompleteSwapOut

The corresponding `protobuf` definition is shown below:

- [MsgCompleteSwapOut](./common/proto.md#msgcompleteswapout)

## SPExit

The corresponding `protobuf` definition is shown below:

- [MsgStorageProviderExit](./common/proto.md#msgstorageproviderexit)

## CompleteSPExit

The corresponding `protobuf` definition is shown below:

- [MsgCompleteStorageProviderExit](./common/proto.md#msgcompletestorageproviderexit)

## GfSp Framework Signer Code

Signer module code implementation: [Signer](https://github.com/mocachain/moca-storage-provider/tree/master/modular/signer)
