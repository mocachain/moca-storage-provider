package piecestore_e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/pkg/log"
	"github.com/mocachain/moca-storage-provider/store/piecestore/piece"
	"github.com/mocachain/moca-storage-provider/store/piecestore/storage"
)

const shardNum = 5

func TestSharding(t *testing.T) {
	dir := t.TempDir()
	handler := setup(t, storage.DiskFileStore, filepath.Join(dir, "shard%d"), storage.AKSKIAMType, shardNum)
	doShardingOperations(t, handler, createFiles(t, dir, shardNum), shardNum)
}

func doShardingOperations(t *testing.T, handler *piece.PieceStore, files []string, shards int) {
	t.Helper()
	for i := 0; i < shards; i++ {
		key := fmt.Sprintf("shards%d.txt", i)
		// 1. put piece
		log.Info("Put piece")
		f, err := os.Open(files[i])
		require.NoError(t, err)
		err = handler.Put(context.Background(), key, f)
		_ = f.Close()
		require.NoError(t, err)

		// 2. get piece
		log.Info("Get piece")
		rc, err := handler.Get(context.Background(), key, 0, -1)
		require.NoError(t, err)
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		require.NoError(t, err)
		assert.Contains(t, string(data), "test sharding func:")

		// 3. head object
		log.Info("Get piece info")
		object, err := handler.Head(context.Background(), key)
		require.NoError(t, err)
		assert.Equal(t, key, object.Key())
		assert.Equal(t, false, object.IsSymlink())
		log.Infow("piece info", "key", object.Key(), "size", object.Size(), "mod time", object.ModTime(),
			"is symlink", object.IsSymlink())

		// 4. delete piece
		log.Info("Delete piece")
		require.NoError(t, handler.Delete(context.Background(), key))
	}
}

func createFiles(t *testing.T, dir string, fileNum int) []string {
	t.Helper()
	files := make([]string, 0, fileNum)
	for i := 0; i < fileNum; i++ {
		name := filepath.Join(dir, fmt.Sprintf("shards%d.txt", i))
		require.NoError(t, os.WriteFile(name, []byte(fmt.Sprintf("test sharding func: %d\n", i)), 0o600))
		files = append(files, name)
	}
	return files
}
