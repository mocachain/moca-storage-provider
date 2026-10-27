package piecestore_e2e

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/pkg/log"
	"github.com/mocachain/moca-storage-provider/store/piecestore/piece"
	"github.com/mocachain/moca-storage-provider/store/piecestore/storage"
)

const (
	pieceKey     = "hello.txt"
	s3BucketURL  = "https://s3.us-east-1.amazonaws.com/test"
	ossBucketURL = "https://example.oss-ap-northeast-1.aliyuncs.com/"
	b2BucketURL  = "https://s3.us-east-005.backblazeb2.com/mocasp"
)

// the cloud piece stores run only where their bucket and credentials are configured
const (
	s3BucketURLEnv    = "PIECESTORE_E2E_S3_BUCKET_URL"
	ossBucketURLEnv   = "PIECESTORE_E2E_OSS_BUCKET_URL"
	minioBucketURLEnv = "PIECESTORE_E2E_MINIO_BUCKET_URL"
	b2BucketURLEnv    = "PIECESTORE_E2E_B2_BUCKET_URL"
)

// bucketURLFromEnv returns the bucket URL configured in the environment, or the fallback.
func bucketURLFromEnv(envName, fallback string) string {
	if value := os.Getenv(envName); value != "" {
		return value
	}
	return fallback
}

// skipUnlessConfigured skips the test unless every environment variable is set.
func skipUnlessConfigured(t *testing.T, store string, envNames ...string) {
	t.Helper()
	var missing []string
	for _, name := range envNames {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Skipf("%s piece store is not configured, set %s to run it", store, strings.Join(missing, ", "))
	}
}

func setup(t *testing.T, storageType, bucketURL, iamType string, shards int) *piece.PieceStore {
	t.Helper()
	handler, err := piece.NewPieceStore(&storage.PieceStoreConfig{
		Shards: shards,
		Store: storage.ObjectStorageConfig{
			Storage:    storageType,
			BucketURL:  bucketURL,
			MaxRetries: 5,
			IAMType:    iamType,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, handler)
	return handler
}

func doOperations(t *testing.T, handler *piece.PieceStore) {
	t.Helper()
	// 1. put piece
	log.Info("Put piece")
	f, err := os.Open("./testdata/hello.txt")
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, handler.Put(context.Background(), pieceKey, f))

	// 2. get piece
	log.Info("Get piece")
	rc, err := handler.Get(context.Background(), pieceKey, 0, -1)
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Hello, World!\n")

	// 3. head object
	log.Info("Head piece info")
	object, err := handler.Head(context.Background(), pieceKey)
	require.NoError(t, err)
	assert.Equal(t, pieceKey, object.Key())
	assert.Equal(t, false, object.IsSymlink())
	log.Infow("piece info", "key", object.Key(), "size", object.Size(), "mod time", object.ModTime(),
		"is symlink", object.IsSymlink())

	// 4. delete piece
	log.Info("Delete piece")
	require.NoError(t, handler.Delete(context.Background(), pieceKey))
}
