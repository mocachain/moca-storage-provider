package storageprovider

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	abci "github.com/cometbft/cometbft/abci/types"
	tmctypes "github.com/cometbft/cometbft/rpc/core/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	sptypes "github.com/evmos/evmos/v12/x/sp/types"
	vgtypes "github.com/evmos/evmos/v12/x/virtualgroup/types"
	"github.com/forbole/juno/v4/log"

	"github.com/forbole/juno/v4/common"
	"github.com/forbole/juno/v4/models"
)

var (
	EventCreateStorageProvider       = proto.MessageName(&sptypes.EventCreateStorageProvider{})
	EventEditStorageProvider         = proto.MessageName(&sptypes.EventEditStorageProvider{})
	EventSpStoragePriceUpdate        = proto.MessageName(&sptypes.EventSpStoragePriceUpdate{})
	EventCompleteSpExit              = proto.MessageName(&vgtypes.EventCompleteStorageProviderExit{})
	EventDeposit                     = proto.MessageName(&sptypes.EventDeposit{})
	EventUpdateStorageProviderStatus = proto.MessageName(&sptypes.EventUpdateStorageProviderStatus{})
)

var StorageProviderEvents = map[string]bool{
	EventCreateStorageProvider:       true,
	EventEditStorageProvider:         true,
	EventSpStoragePriceUpdate:        true,
	EventCompleteSpExit:              true,
	EventDeposit:                     true,
	EventUpdateStorageProviderStatus: true,
}

func (m *Module) ExtractEventStatements(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, event sdk.Event) (map[string][]interface{}, error) {
	if !StorageProviderEvents[event.Type] {
		return nil, nil
	}

	typedEvent, err := sdk.ParseTypedEvent(abci.Event(event))
	if err != nil {
		log.Errorw("parse typed events error", "module", m.Name(), "event", event, "err", err)
		return nil, err
	}

	switch event.Type {
	case EventCreateStorageProvider:
		createStorageProvider, ok := typedEvent.(*sptypes.EventCreateStorageProvider)
		if !ok {
			log.Errorw("type assert error", "type", "EventCreateStorageProvider", "event", typedEvent)
			return nil, errors.New("create storage provider event assert error")
		}
		return m.handleCreateStorageProvider(ctx, block, txHash, createStorageProvider), nil
	case EventEditStorageProvider:
		editStorageProvider, ok := typedEvent.(*sptypes.EventEditStorageProvider)
		if !ok {
			log.Errorw("type assert error", "type", "EventEditStorageProvider", "event", typedEvent)
			return nil, errors.New("edit storage provider event assert error")
		}
		return m.handleEditStorageProvider(ctx, block, txHash, editStorageProvider), nil
	case EventSpStoragePriceUpdate:
		spStoragePriceUpdate, ok := typedEvent.(*sptypes.EventSpStoragePriceUpdate)
		if !ok {
			log.Errorw("type assert error", "type", "EventSpStoragePriceUpdate", "event", typedEvent)
			return nil, errors.New("storage provider price update event assert error")
		}
		return m.handleSpStoragePriceUpdate(ctx, block, txHash, spStoragePriceUpdate), nil
	case EventCompleteSpExit:
		completeSpExit, ok := typedEvent.(*vgtypes.EventCompleteStorageProviderExit)
		if !ok {
			log.Errorw("type assert error", "type", "EventCompleteSpExit", "event", typedEvent)
			return nil, errors.New("storage provider exit event assert error")
		}
		return m.handleCompleteStorageProviderExit(ctx, block, txHash, completeSpExit), nil
	case EventUpdateStorageProviderStatus:
		updateStorageProviderStatus, ok := typedEvent.(*sptypes.EventUpdateStorageProviderStatus)
		if !ok {
			log.Errorw("type assert error", "type", "EventUpdateStorageProviderStatus", "event", typedEvent)
			return nil, errors.New("storage provider status event assert error")
		}
		return m.handleUpdateStorageProviderStatus(ctx, block, txHash, updateStorageProviderStatus), nil
	case EventDeposit:
		deposit, ok := typedEvent.(*sptypes.EventDeposit)
		if !ok {
			log.Errorw("type assert error", "type", "EventDeposit", "event", typedEvent)
			return nil, errors.New("storage provider deposit event assert error")
		}
		return m.handleDeposit(ctx, block, txHash, deposit)
	}

	return nil, nil
}

func (m *Module) HandleEvent(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, event sdk.Event) error {
	return nil
}

func (m *Module) handleCreateStorageProvider(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, createStorageProvider *sptypes.EventCreateStorageProvider) map[string][]interface{} {
	storageProvider := &models.StorageProvider{
		SpId:            createStorageProvider.SpId,
		OperatorAddress: common.HexToAddress(createStorageProvider.SpAddress),
		FundingAddress:  common.HexToAddress(createStorageProvider.FundingAddress),
		SealAddress:     common.HexToAddress(createStorageProvider.SealAddress),
		ApprovalAddress: common.HexToAddress(createStorageProvider.ApprovalAddress),
		GcAddress:       common.HexToAddress(createStorageProvider.GcAddress),
		TotalDeposit:    (*common.Big)(createStorageProvider.TotalDeposit.Amount.BigInt()),
		Status:          createStorageProvider.Status.String(),
		Endpoint:        createStorageProvider.Endpoint,
		Moniker:         createStorageProvider.Description.Moniker,
		Identity:        createStorageProvider.Description.Identity,
		Website:         createStorageProvider.Description.Website,
		SecurityContact: createStorageProvider.Description.SecurityContact,
		Details:         createStorageProvider.Description.Details,
		BlsKey:          createStorageProvider.BlsKey,

		CreateTxHash: txHash,
		CreateAt:     block.Block.Height,
		UpdateAt:     block.Block.Height,
		UpdateTxHash: txHash,
		Removed:      false,
	}

	k, v := m.db.CreateStorageProviderToSQL(ctx, storageProvider)
	return map[string][]interface{}{
		k: v,
	}
}

func (m *Module) handleEditStorageProvider(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, editStorageProvider *sptypes.EventEditStorageProvider) map[string][]interface{} {
	storageProvider := &models.StorageProvider{
		SpId:            editStorageProvider.SpId,
		OperatorAddress: common.HexToAddress(editStorageProvider.SpAddress),
		SealAddress:     common.HexToAddress(editStorageProvider.SealAddress),
		ApprovalAddress: common.HexToAddress(editStorageProvider.ApprovalAddress),
		GcAddress:       common.HexToAddress(editStorageProvider.GcAddress),
		Endpoint:        editStorageProvider.Endpoint,
		Moniker:         editStorageProvider.Description.Moniker,
		Identity:        editStorageProvider.Description.Identity,
		Website:         editStorageProvider.Description.Website,
		SecurityContact: editStorageProvider.Description.SecurityContact,
		Details:         editStorageProvider.Description.Details,
		BlsKey:          editStorageProvider.BlsKey,

		UpdateAt:     block.Block.Height,
		UpdateTxHash: txHash,
		Removed:      false,
	}

	k, v := m.db.UpdateStorageProviderToSQL(ctx, storageProvider)
	return map[string][]interface{}{
		k: v,
	}
}

func (m *Module) handleSpStoragePriceUpdate(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, spStoragePriceUpdate *sptypes.EventSpStoragePriceUpdate) map[string][]interface{} {
	storageProvider := &models.StorageProvider{
		SpId:          spStoragePriceUpdate.SpId,
		UpdateTimeSec: spStoragePriceUpdate.UpdateTimeSec,
		ReadPrice:     (*common.Big)(spStoragePriceUpdate.ReadPrice.BigInt()),
		FreeReadQuota: spStoragePriceUpdate.FreeReadQuota,
		StorePrice:    (*common.Big)(spStoragePriceUpdate.StorePrice.BigInt()),

		UpdateAt:     block.Block.Height,
		UpdateTxHash: txHash,
		Removed:      false,
	}

	k, v := m.db.UpdateStorageProviderToSQL(ctx, storageProvider)
	return map[string][]interface{}{
		k: v,
	}
}

func (m *Module) handleCompleteStorageProviderExit(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, completeStorageProviderExit *vgtypes.EventCompleteStorageProviderExit) map[string][]interface{} {
	data := &models.StorageProvider{
		SpId: completeStorageProviderExit.StorageProviderId,

		UpdateAt:     block.Block.Height,
		UpdateTxHash: txHash,
		Removed:      true,
	}
	k, v := m.db.UpdateStorageProviderToSQL(ctx, data)
	return map[string][]interface{}{
		k: v,
	}
}

func (m *Module) handleUpdateStorageProviderStatus(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, updateStorageProviderStatus *sptypes.EventUpdateStorageProviderStatus) map[string][]interface{} {
	storageProvider := &models.StorageProvider{
		SpId:         updateStorageProviderStatus.SpId,
		Status:       updateStorageProviderStatus.NewStatus,
		UpdateAt:     block.Block.Height,
		UpdateTxHash: txHash,
		Removed:      false,
	}
	k, v := m.db.UpdateStorageProviderToSQL(ctx, storageProvider)
	return map[string][]interface{}{
		k: v,
	}
}

func (m *Module) handleDeposit(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, deposit *sptypes.EventDeposit) (map[string][]interface{}, error) {
	totalDeposit := new(big.Int)
	if _, ok := totalDeposit.SetString(deposit.TotalDeposit, 10); !ok {
		return nil, fmt.Errorf("invalid total_deposit %q", deposit.TotalDeposit)
	}

	storageProvider := &models.StorageProvider{
		FundingAddress: common.HexToAddress(deposit.FundingAddress),
		TotalDeposit:   (*common.Big)(totalDeposit),
		UpdateAt:       block.Block.Height,
		UpdateTxHash:   txHash,
		Removed:        false,
	}

	k, v := m.db.UpdateStorageProviderByFundingAddressToSQL(ctx, storageProvider)
	return map[string][]interface{}{
		k: v,
	}, nil
}
