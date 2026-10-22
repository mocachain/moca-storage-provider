package manager

import (
	"context"
	"fmt"
	"testing"

	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	storeclient "github.com/mocachain/moca-storage-provider/store/piecestore/client"
	"github.com/mocachain/moca-storage-provider/store/piecestore/storage"
	"github.com/stretchr/testify/require"
)

func TestRecoveryFenceExactKeysOnDiskAndShardedDisk(t *testing.T) {
	for _, shards := range []int{0, 4} {
		t.Run(fmt.Sprint(shards), func(t *testing.T) {
			m, object, params, _, _, _, _ := recoveryFixture(t, -1)
			bucket := t.TempDir()
			if shards > 1 {
				bucket += "/shard-%d"
			}
			store, err := storeclient.NewStoreClient(&storage.PieceStoreConfig{Shards: shards, Store: storage.ObjectStorageConfig{Storage: storage.DiskFileStore, BucketURL: bucket, IAMType: storage.AKSKIAMType}})
			require.NoError(t, err)
			task := &gfsptask.GfSpRecoverPieceTask{}
			task.InitRecoverPieceTask(object, params, 85, 0, -1, 4, 50, 5)
			require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
			old, err := m.dispatchRecovery(context.Background(), task)
			require.NoError(t, err)
			current, err := m.dispatchRecovery(context.Background(), old)
			require.NoError(t, err)
			oldKey, err := gfsptask.RecoveryStageKey(old)
			require.NoError(t, err)
			key, err := gfsptask.RecoveryStageKey(current)
			require.NoError(t, err)
			canonical := "s91_s0_v2"
			for _, k := range []string{oldKey, key, canonical} {
				require.NoError(t, store.PutPiece(context.Background(), k, []byte("body")))
			}
			require.NoError(t, store.DeletePiece(context.Background(), oldKey))
			for _, k := range []string{key, canonical} {
				data, err := store.GetPiece(context.Background(), k, 0, 5)
				require.NoError(t, err)
				require.Equal(t, []byte("body"), data)
			}
			// A late isolated writer may recreate only its orphan; it cannot change the
			// current stage or canonical key. Capacity claims deliberately survive this.
			require.NoError(t, store.PutPiece(context.Background(), oldKey, []byte("late")))
			data, err := store.GetPiece(context.Background(), canonical, 0, 5)
			require.NoError(t, err)
			require.Equal(t, []byte("body"), data)
		})
	}
}
