package piecestore_e2e

import (
	"path/filepath"
	"testing"

	"github.com/mocachain/moca-storage-provider/store/piecestore/storage"
)

func TestS3Store(t *testing.T) {
	skipUnlessConfigured(t, "s3", storage.AWSAccessKey, storage.AWSSecretKey)
	handler := setup(t, storage.S3Store, bucketURLFromEnv(s3BucketURLEnv, s3BucketURL), storage.AKSKIAMType, 0)
	doOperations(t, handler)
}

func TestOSSStore(t *testing.T) {
	skipUnlessConfigured(t, "oss", storage.OSSRoleARN, storage.OSSOidcProviderArn, storage.OSSWebIdentityTokenFile)
	handler := setup(t, storage.OSSStore, bucketURLFromEnv(ossBucketURLEnv, ossBucketURL), storage.SAIAMType, 0)
	doOperations(t, handler)
}

func TestMinioStore(t *testing.T) {
	skipUnlessConfigured(t, "minio", minioBucketURLEnv, storage.MinioAccessKey, storage.MinioSecretKey)
	handler := setup(t, storage.MinioStore, bucketURLFromEnv(minioBucketURLEnv, ""), storage.AKSKIAMType, 0)
	doOperations(t, handler)
}

func TestB2Store(t *testing.T) {
	skipUnlessConfigured(t, "b2", storage.B2AccessKey, storage.B2SecretKey)
	handler := setup(t, storage.B2Store, bucketURLFromEnv(b2BucketURLEnv, b2BucketURL), storage.AKSKIAMType, 0)
	doOperations(t, handler)
}

func TestDiskFileStore(t *testing.T) {
	handler := setup(t, storage.DiskFileStore, t.TempDir(), storage.AKSKIAMType, 0)
	doOperations(t, handler)
}

func TestMemoryStore(t *testing.T) {
	handler := setup(t, storage.MemoryStore, "", storage.AKSKIAMType, 0)
	doOperations(t, handler)
}

func TestStoresIgnoreTheBucketURLEnvironment(t *testing.T) {
	t.Setenv(storage.BucketURL, filepath.Join(t.TempDir(), "elsewhere"))
	handler := setup(t, storage.DiskFileStore, t.TempDir(), storage.AKSKIAMType, 0)
	doOperations(t, handler)
}
