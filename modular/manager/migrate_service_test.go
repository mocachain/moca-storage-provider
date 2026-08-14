package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	storetypes "github.com/mocachain/moca-storage-provider/store/types"
)

// TestManageModular_NotifyPostMigrateBucketAndRecoupQuota_PropagatesRecoupError
// covers a RecoupQuota failure: the function must return that error, not the
// stale (nil) err left over from the earlier getBucketTotalSize call.
func TestManageModular_NotifyPostMigrateBucketAndRecoupQuota_PropagatesRecoupError(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)

	db := spdb.NewMockSPDB(ctrl)
	m.baseApp.SetGfSpDB(db)
	db.EXPECT().QueryMigrateBucketState(gomock.Any()).Return(
		int(storetypes.BucketMigrationState_BUCKET_MIGRATION_STATE_SRC_SP_PRE_DEDUCT_QUOTA_DONE), nil).Times(1)

	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(client)
	client.EXPECT().GetLatestBucketReadQuota(gomock.Any(), gomock.Any()).Return(
		&gfsptask.GfSpBucketQuotaInfo{Month: "2024-01"}, nil).Times(1)
	client.EXPECT().GetBucketSize(gomock.Any(), gomock.Any()).Return("100", nil).Times(1)
	client.EXPECT().RecoupQuota(gomock.Any(), gomock.Any(), uint64(50), "2024-01").Return(mockErr).Times(1)

	bmInfo := &gfsptask.GfSpBucketMigrationInfo{
		BucketId:          1,
		Finished:          false,
		MigratedBytesSize: 50,
	}

	_, err := m.NotifyPostMigrateBucketAndRecoupQuota(context.Background(), bmInfo)
	assert.ErrorIs(t, err, mockErr)
}

// TestManageModular_NotifyPostMigrateBucketAndRecoupQuota_PropagatesStateWriteError
// covers a failure to persist BUCKET_MIGRATION_STATE_MIGRATION_FINISHED after a
// successful RecoupQuota. The function must return that error rather than
// falling through to (latestQuota, nil): the migration-state guard at the top
// of this function never advanced, so a caller that treats this as success
// (and doesn't retry) leaves the bucket able to be recouped a second time on
// any later re-invocation.
func TestManageModular_NotifyPostMigrateBucketAndRecoupQuota_PropagatesStateWriteError(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)

	db := spdb.NewMockSPDB(ctrl)
	m.baseApp.SetGfSpDB(db)
	db.EXPECT().QueryMigrateBucketState(gomock.Any()).Return(
		int(storetypes.BucketMigrationState_BUCKET_MIGRATION_STATE_SRC_SP_PRE_DEDUCT_QUOTA_DONE), nil).Times(1)
	db.EXPECT().UpdateBucketMigrationRecoupQuota(gomock.Any(), uint64(50),
		int(storetypes.BucketMigrationState_BUCKET_MIGRATION_STATE_MIGRATION_FINISHED)).Return(mockErr).Times(1)

	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(client)
	client.EXPECT().GetLatestBucketReadQuota(gomock.Any(), gomock.Any()).Return(
		&gfsptask.GfSpBucketQuotaInfo{Month: "2024-01"}, nil).Times(1)
	client.EXPECT().GetBucketSize(gomock.Any(), gomock.Any()).Return("100", nil).Times(1)
	client.EXPECT().RecoupQuota(gomock.Any(), gomock.Any(), uint64(50), "2024-01").Return(nil).Times(1)

	bmInfo := &gfsptask.GfSpBucketMigrationInfo{
		BucketId:          1,
		Finished:          false,
		MigratedBytesSize: 50,
	}

	_, err := m.NotifyPostMigrateBucketAndRecoupQuota(context.Background(), bmInfo)
	assert.ErrorIs(t, err, mockErr)
}

// TestManageModular_NotifyPostMigrateBucketAndRecoupQuota_Success covers the
// happy path: extraQuota is recouped and the migration progress is recorded.
func TestManageModular_NotifyPostMigrateBucketAndRecoupQuota_Success(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)

	db := spdb.NewMockSPDB(ctrl)
	m.baseApp.SetGfSpDB(db)
	db.EXPECT().QueryMigrateBucketState(gomock.Any()).Return(
		int(storetypes.BucketMigrationState_BUCKET_MIGRATION_STATE_SRC_SP_PRE_DEDUCT_QUOTA_DONE), nil).Times(1)
	db.EXPECT().UpdateBucketMigrationRecoupQuota(gomock.Any(), uint64(50),
		int(storetypes.BucketMigrationState_BUCKET_MIGRATION_STATE_MIGRATION_FINISHED)).Return(nil).Times(1)

	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(client)
	client.EXPECT().GetLatestBucketReadQuota(gomock.Any(), gomock.Any()).Return(
		&gfsptask.GfSpBucketQuotaInfo{Month: "2024-01"}, nil).Times(1)
	client.EXPECT().GetBucketSize(gomock.Any(), gomock.Any()).Return("100", nil).Times(1)
	client.EXPECT().RecoupQuota(gomock.Any(), gomock.Any(), uint64(50), "2024-01").Return(nil).Times(1)

	bmInfo := &gfsptask.GfSpBucketMigrationInfo{
		BucketId:          1,
		Finished:          false,
		MigratedBytesSize: 50,
	}

	_, err := m.NotifyPostMigrateBucketAndRecoupQuota(context.Background(), bmInfo)
	assert.NoError(t, err)
}
