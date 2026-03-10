package group

import (
	"context"
	"errors"

	abci "github.com/cometbft/cometbft/abci/types"
	tmctypes "github.com/cometbft/cometbft/rpc/core/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/forbole/juno/v4/common"
	"github.com/forbole/juno/v4/log"
	"github.com/forbole/juno/v4/models"

	blockcommon "github.com/mocachain/moca-storage-provider/modular/blocksyncer/modules/common"
	storagetypes "github.com/evmos/evmos/v12/x/storage/types"
)

var (
	EventCreateGroup       = proto.MessageName(&storagetypes.EventCreateGroup{})
	EventDeleteGroup       = proto.MessageName(&storagetypes.EventDeleteGroup{})
	EventLeaveGroup        = proto.MessageName(&storagetypes.EventLeaveGroup{})
	EventUpdateGroupMember = proto.MessageName(&storagetypes.EventUpdateGroupMember{})
	EventRenewGroupMember  = proto.MessageName(&storagetypes.EventRenewGroupMember{})
	EventUpdateGroupExtra  = proto.MessageName(&storagetypes.EventUpdateGroupExtra{})
	EventMirrorGroup       = proto.MessageName(&storagetypes.EventMirrorGroup{})
	EventMirrorGroupResult = proto.MessageName(&storagetypes.EventMirrorGroupResult{})
)

var GroupEvents = map[string]bool{
	EventCreateGroup:       true,
	EventDeleteGroup:       true,
	EventLeaveGroup:        true,
	EventUpdateGroupMember: true,
	EventRenewGroupMember:  true,
	EventUpdateGroupExtra:  true,
	EventMirrorGroup:       true,
	EventMirrorGroupResult: true,
}

func (m *Module) ExtractEventStatements(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, event sdk.Event) (map[string][]interface{}, error) {
	if !GroupEvents[event.Type] {
		return nil, nil
	}

	typedEvent, err := sdk.ParseTypedEvent(abci.Event(event))
	if err != nil {
		log.Errorw("parse typed events error", "module", m.Name(), "event", event, "err", err)
		return nil, err
	}

	switch event.Type {
	case EventCreateGroup:
		createGroup, ok := typedEvent.(*storagetypes.EventCreateGroup)
		if !ok {
			log.Errorw("type assert error", "type", "EventCreateGroup", "event", typedEvent)
			return nil, errors.New("create group event assert error")
		}
		return m.handleCreateGroup(ctx, block, createGroup), nil
	case EventUpdateGroupMember:
		updateGroupMember, ok := typedEvent.(*storagetypes.EventUpdateGroupMember)
		if !ok {
			log.Errorw("type assert error", "type", "EventUpdateGroupMember", "event", typedEvent)
			return nil, errors.New("update group member event assert error")
		}
		return m.handleUpdateGroupMember(ctx, block, updateGroupMember), nil

	case EventDeleteGroup:
		deleteGroup, ok := typedEvent.(*storagetypes.EventDeleteGroup)
		if !ok {
			log.Errorw("type assert error", "type", "EventDeleteGroup", "event", typedEvent)
			return nil, errors.New("delete group event assert error")
		}
		return m.handleDeleteGroup(ctx, block, deleteGroup), nil
	case EventLeaveGroup:
		leaveGroup, ok := typedEvent.(*storagetypes.EventLeaveGroup)
		if !ok {
			log.Errorw("type assert error", "type", "EventLeaveGroup", "event", typedEvent)
			return nil, errors.New("leave group event assert error")
		}
		return m.handleLeaveGroup(ctx, block, leaveGroup), nil
	case EventRenewGroupMember:
		renewGroupMember, ok := typedEvent.(*storagetypes.EventRenewGroupMember)
		if !ok {
			log.Errorw("type assert error", "type", "EventRenewGroupMember", "event", typedEvent)
			return nil, errors.New("renew group member event assert error")
		}
		return m.handleRenewGroupMember(ctx, block, renewGroupMember), nil
	case EventUpdateGroupExtra:
		updateGroupExtra, ok := typedEvent.(*storagetypes.EventUpdateGroupExtra)
		if !ok {
			log.Errorw("type assert error", "type", "EventUpdateGroupExtra", "event", typedEvent)
			return nil, errors.New("update group extra event assert error")
		}
		return m.handleUpdateGroupExtra(ctx, block, txHash, updateGroupExtra), nil
	case EventMirrorGroup:
		mirrorGroup, ok := typedEvent.(*storagetypes.EventMirrorGroup)
		if !ok {
			log.Errorw("type assert error", "type", "EventMirrorGroup", "event", typedEvent)
			return nil, errors.New("mirror group event assert error")
		}
		return m.handleMirrorGroup(ctx, block, txHash, mirrorGroup), nil
	case EventMirrorGroupResult:
		mirrorGroupResult, ok := typedEvent.(*storagetypes.EventMirrorGroupResult)
		if !ok {
			log.Errorw("type assert error", "type", "EventMirrorGroupResult", "event", typedEvent)
			return nil, errors.New("mirror group result event assert error")
		}
		return m.handleMirrorGroupResult(ctx, block, txHash, mirrorGroupResult), nil
	}
	return nil, nil
}

func (m *Module) HandleEvent(ctx context.Context, block *tmctypes.ResultBlock, _ common.Hash, event sdk.Event) error {
	return nil
}

func (m *Module) handleCreateGroup(ctx context.Context, block *tmctypes.ResultBlock, createGroup *storagetypes.EventCreateGroup) map[string][]interface{} {

	var membersToAddList []*models.Group

	//create group first
	groupItem := &models.Group{
		Owner:      common.HexToAddress(createGroup.Owner),
		GroupID:    common.BigToHash(createGroup.GroupId.BigInt()),
		GroupName:  createGroup.GroupName,
		SourceType: createGroup.SourceType.String(),
		AccountID:  common.HexToAddress("0"),
		Extra:      createGroup.Extra,

		CreateAt:   block.Block.Height,
		CreateTime: block.Block.Time.UTC().Unix(),
		UpdateAt:   block.Block.Height,
		UpdateTime: block.Block.Time.UTC().Unix(),
		Removed:    false,
	}
	membersToAddList = append(membersToAddList, groupItem)

	k, v := m.db.CreateGroupToSQL(ctx, membersToAddList)
	return map[string][]interface{}{
		k: v,
	}
}

func (m *Module) handleDeleteGroup(ctx context.Context, block *tmctypes.ResultBlock, deleteGroup *storagetypes.EventDeleteGroup) map[string][]interface{} {
	group := &models.Group{
		Owner:     common.HexToAddress(deleteGroup.Owner),
		GroupID:   common.BigToHash(deleteGroup.GroupId.BigInt()),
		GroupName: deleteGroup.GroupName,

		UpdateAt:   block.Block.Height,
		UpdateTime: block.Block.Time.UTC().Unix(),
		Removed:    true,
	}

	res := make(map[string][]interface{})

	k, v := m.db.DeleteGroupToSQL(ctx, group)
	res[k] = v
	return res
}

func (m *Module) handleLeaveGroup(ctx context.Context, block *tmctypes.ResultBlock, leaveGroup *storagetypes.EventLeaveGroup) map[string][]interface{} {
	group := &models.Group{
		Owner:     common.HexToAddress(leaveGroup.Owner),
		GroupID:   common.BigToHash(leaveGroup.GroupId.BigInt()),
		GroupName: leaveGroup.GroupName,
		AccountID: common.HexToAddress(leaveGroup.MemberAddress),

		UpdateAt:   block.Block.Height,
		UpdateTime: block.Block.Time.UTC().Unix(),
		Removed:    true,
	}

	//update group item
	groupItem := &models.Group{
		GroupID:   common.BigToHash(leaveGroup.GroupId.BigInt()),
		AccountID: common.HexToAddress("0"),

		UpdateAt:   block.Block.Height,
		UpdateTime: block.Block.Time.UTC().Unix(),
		Removed:    false,
	}
	res := make(map[string][]interface{})
	k, v := m.db.UpdateGroupToSQL(ctx, groupItem)
	res[k] = v

	k, v = m.db.UpdateGroupToSQL(ctx, group)
	res[k] = v
	return res
}

func (m *Module) handleUpdateGroupMember(ctx context.Context, block *tmctypes.ResultBlock, updateGroupMember *storagetypes.EventUpdateGroupMember) map[string][]interface{} {

	membersToAdd := updateGroupMember.MembersToAdd
	membersToDelete := updateGroupMember.MembersToDelete

	var membersToAddList []*models.Group
	res := make(map[string][]interface{})

	if len(membersToAdd) > 0 {
		for _, memberToAdd := range membersToAdd {
			groupItem := &models.Group{
				Owner:     common.HexToAddress(updateGroupMember.Owner),
				GroupID:   common.BigToHash(updateGroupMember.GroupId.BigInt()),
				GroupName: updateGroupMember.GroupName,
				AccountID: common.HexToAddress(memberToAdd.Member),
				Operator:  common.HexToAddress(updateGroupMember.Operator),

				CreateAt:   block.Block.Height,
				CreateTime: block.Block.Time.UTC().Unix(),
				UpdateAt:   block.Block.Height,
				UpdateTime: block.Block.Time.UTC().Unix(),
				Removed:    false,
			}
			if memberToAdd.ExpirationTime != nil {
				groupItem.ExpirationTime = memberToAdd.ExpirationTime.Unix()
			}
			membersToAddList = append(membersToAddList, groupItem)
		}
		k, v := m.db.CreateGroupToSQL(ctx, membersToAddList)
		res[k] = v
	}

	if len(membersToDelete) > 0 {
		groupItem := &models.Group{
			Operator: common.HexToAddress(updateGroupMember.Operator),

			UpdateAt:   block.Block.Height,
			UpdateTime: block.Block.Time.UTC().Unix(),
			Removed:    true,
		}
		accountIDs := make([]common.Address, 0, len(membersToDelete))
		for _, memberToDelete := range membersToDelete {
			accountIDs = append(accountIDs, common.HexToAddress(memberToDelete))
		}
		k, v := m.db.BatchDeleteGroupMemberToSQL(ctx, groupItem, common.BigToHash(updateGroupMember.GroupId.BigInt()), accountIDs)
		res[k] = v
	}

	//update group item
	groupItem := &models.Group{
		GroupID:   common.BigToHash(updateGroupMember.GroupId.BigInt()),
		AccountID: common.HexToAddress("0"),

		UpdateAt:   block.Block.Height,
		UpdateTime: block.Block.Time.UTC().Unix(),
		Removed:    false,
	}
	k, v := m.db.UpdateGroupToSQL(ctx, groupItem)
	res[k] = v

	return res
}

func (m *Module) handleRenewGroupMember(ctx context.Context, block *tmctypes.ResultBlock, renewGroupMember *storagetypes.EventRenewGroupMember) map[string][]interface{} {
	res := map[string][]interface{}{}
	for _, e := range renewGroupMember.Members {
		expirationTime := int64(0)
		if e.ExpirationTime != nil {
			expirationTime = e.ExpirationTime.Unix()
		}
		k := "Update `groups` set expiration_time = ?, update_at = ?, update_time = ? where account_id = ? and group_id = ?"
		v := []interface{}{expirationTime, block.Block.Height, block.Block.Time.UTC().Unix(), common.HexToAddress(e.Member), common.BigToHash(renewGroupMember.GroupId.BigInt())}
		res[k] = v
	}

	return res
}

func (m *Module) handleUpdateGroupExtra(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, updateGroupExtra *storagetypes.EventUpdateGroupExtra) map[string][]interface{} {
	groupItem := &models.Group{
		GroupID:    common.BigToHash(updateGroupExtra.GroupId.BigInt()),
		AccountID:  common.HexToAddress("0"),
		Operator:   common.HexToAddress(updateGroupExtra.Operator),
		Extra:      updateGroupExtra.Extra,
		UpdateAt:   block.Block.Height,
		UpdateTime: block.Block.Time.UTC().Unix(),
		Removed:    false,
	}

	k, v := m.db.UpdateGroupToSQL(ctx, groupItem)
	return map[string][]interface{}{
		k: v,
	}
}

func (m *Module) handleMirrorGroup(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, mirrorGroup *storagetypes.EventMirrorGroup) map[string][]interface{} {
	groupItem := &models.Group{
		GroupID:    common.BigToHash(mirrorGroup.GroupId.BigInt()),
		AccountID:  common.HexToAddress("0"),
		SourceType: storagetypes.SOURCE_TYPE_MIRROR_PENDING.String(),
		UpdateAt:   block.Block.Height,
		UpdateTime: block.Block.Time.UTC().Unix(),
		Removed:    false,
	}

	k, v := m.db.UpdateGroupToSQL(ctx, groupItem)
	return map[string][]interface{}{
		k: v,
	}
}

func (m *Module) handleMirrorGroupResult(ctx context.Context, block *tmctypes.ResultBlock, txHash common.Hash, mirrorGroupResult *storagetypes.EventMirrorGroupResult) map[string][]interface{} {
	sourceType := storagetypes.SOURCE_TYPE_ORIGIN.String()
	if mirrorGroupResult.Status == 0 {
		if mapped, ok := blockcommon.MapDestChainIDToSourceType(mirrorGroupResult.DestChainId); ok {
			sourceType = mapped.String()
		}
	}

	groupItem := &models.Group{
		GroupID:    common.BigToHash(mirrorGroupResult.GroupId.BigInt()),
		AccountID:  common.HexToAddress("0"),
		SourceType: sourceType,
		UpdateAt:   block.Block.Height,
		UpdateTime: block.Block.Time.UTC().Unix(),
		Removed:    false,
	}

	k, v := m.db.UpdateGroupToSQL(ctx, groupItem)
	return map[string][]interface{}{
		k: v,
	}
}
