package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const mockFileBucket = "fileBucket"

func setupDiskFileTest(t *testing.T) *diskFileStore {
	return &diskFileStore{root: emptyString}
}

func createTempFile(t *testing.T) *os.File {
	tmpdir := t.TempDir()
	fmt.Println(tmpdir)
	f, _ := os.CreateTemp(tmpdir, "test_disk_file.txt")
	f.Write([]byte("Hello"))
	defer func() {
		_ = f.Close()
	}()
	return f
}

func TestDiskFileStore_String(t *testing.T) {
	store := &diskFileStore{root: mockFileBucket}
	result := store.String()
	if runtime.GOOS == windowsOS {
		assert.Equal(t, "file:///fileBucket", result)
	}
	assert.Equal(t, "file://fileBucket", result)
}

func TestDiskFileStore_CreateBucket(t *testing.T) {
	store := &diskFileStore{root: mockFileBucket}
	defer os.Remove(mockFileBucket)
	err := store.CreateBucket(context.TODO())
	assert.Nil(t, err)
}

func TestDiskFileStore_GetObjectSuccess(t *testing.T) {
	f := createTempFile(t)
	tmpdir := t.TempDir()
	cases := []struct {
		name         string
		key          string
		offset       int64
		limit        int64
		wantedResult string
		wantedErr    error
	}{
		{
			name:         "disk_file_get_without_offset_limit_test1",
			key:          f.Name(),
			wantedResult: "Hello",
			wantedErr:    nil,
		},
		{
			name:         "disk_file_get_file_test1",
			limit:        4,
			key:          f.Name(),
			wantedResult: "Hell",
			wantedErr:    nil,
		},
		{
			name:         "disk_file_get_dir_test1",
			key:          tmpdir,
			wantedResult: emptyString,
			wantedErr:    nil,
		},
		{
			name:         "disk_file_get_with_offset_limit_test1",
			key:          f.Name(),
			offset:       3,
			limit:        2,
			wantedResult: "lo",
			wantedErr:    nil,
		},
		{
			name:         "disk_file_get_with_huge_limit_test1",
			key:          f.Name(),
			offset:       3,
			limit:        int64(^uint64(0) >> 1),
			wantedResult: "lo",
			wantedErr:    nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := setupDiskFileTest(t)
			data, err := store.GetObject(context.TODO(), tt.key, tt.offset, tt.limit)
			assert.Equal(t, tt.wantedErr, err)
			data1, err := io.ReadAll(data)
			if err != nil {
				t.Fatalf("io ReadAll error: %s", err)
			}
			assert.Equal(t, tt.wantedResult, string(data1))
		})
	}
}

func TestDiskFileStore_GetObjectFailure(t *testing.T) {
	f := createTempFile(t)
	cases := []struct {
		name      string
		key       string
		offset    int64
		limit     int64
		wantedErr error
	}{
		{
			name:      "disk_file_get_non_existed_test1",
			key:       "test_file",
			wantedErr: errors.New("open test_file: no such file or directory"),
		},
		{
			name:      "disk_file_get_with_error_offset_test1",
			key:       f.Name(),
			offset:    6,
			limit:     1,
			wantedErr: errors.New("EOF"),
		},
		{
			name:      "disk_file_get_with_huge_limit_past_end_test1",
			key:       f.Name(),
			offset:    6,
			limit:     int64(^uint64(0) >> 1),
			wantedErr: errors.New("EOF"),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := setupDiskFileTest(t)
			data, err := store.GetObject(context.TODO(), tt.key, tt.offset, tt.limit)
			assert.Equal(t, tt.wantedErr.Error(), err.Error())
			assert.Equal(t, nil, data)
		})
	}
}

func TestDiskFileStore_PutObjectSuccess(t *testing.T) {
	f := createTempFile(t)
	defer func() {
		_ = os.Remove("test/")
	}()
	cases := []struct {
		name         string
		key          string
		data         string
		cfr          bool
		wantedResult string
		wantedErr    error
	}{
		{
			name:      "disk_file_put_cfr_true_success_test1",
			data:      mockSessionToken,
			cfr:       true,
			key:       f.Name(),
			wantedErr: nil,
		},
		{
			name:      "disk_file_put_cfr_false_success_test2",
			key:       f.Name(),
			data:      mockSessionToken,
			cfr:       false,
			wantedErr: nil,
		},
		{
			name:      "disk_file_put_with_dir_suffix_success_test3",
			key:       "test/",
			data:      mockSessionToken,
			cfr:       false,
			wantedErr: nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := setupDiskFileTest(t)
			err := store.PutObject(context.TODO(), tt.key, strings.NewReader(tt.data))
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func TestDiskFileStore_PutObjectFailure(t *testing.T) {
	defer func() {
		fileList, err := filepath.Glob("...tmp*")
		if err != nil {
			t.Fatalf("filepath.Glob error: %s", err)
		}
		_ = os.Remove(fileList[0])
	}()
	cases := []struct {
		name         string
		key          string
		data         string
		cfr          bool
		wantedResult string
		wantedErr    error
	}{
		{
			name:      "disk_file_put_error_test1",
			data:      mockSessionToken,
			cfr:       true,
			key:       emptyString,
			wantedErr: errors.New("no such file or directory"),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := setupDiskFileTest(t)
			err := store.PutObject(context.TODO(), tt.key, strings.NewReader(tt.data))
			assert.Equal(t, true, strings.Contains(err.Error(), tt.wantedErr.Error()))
		})
	}
}

func TestDiskFileStore_DeleteObject(t *testing.T) {
	f := createTempFile(t)
	cases := []struct {
		name         string
		key          string
		wantedResult string
		wantedErr    error
	}{
		{
			name:      "disk_file_delete_success_test1",
			key:       f.Name(),
			wantedErr: nil,
		},
		{
			name:      "disk_file_delete_error_test1",
			key:       "piece_store",
			wantedErr: nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := setupDiskFileTest(t)
			err := store.DeleteObject(context.TODO(), tt.key)
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func TestDiskFileStore_HeadBucket(t *testing.T) {
	store := &diskFileStore{root: "./const.go"}
	err := store.HeadBucket(context.TODO())
	assert.Nil(t, err)
}

func TestDiskFileStore_HeadObject(t *testing.T) {
	f := createTempFile(t)
	cases := []struct {
		name         string
		key          string
		offset       int64
		limit        int64
		wantedResult interface{}
		wantedIsErr  bool
		wantedErrStr string
	}{
		{
			name:         "disk_file_head_success_test1",
			key:          f.Name(),
			wantedResult: f.Name(),
			wantedIsErr:  false,
			wantedErrStr: "",
		},
		{
			name:         "disk_file_head_failure",
			key:          "./text.txt",
			wantedResult: f.Name(),
			wantedIsErr:  true,
			wantedErrStr: "no such file or directory",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := setupDiskFileTest(t)
			obj, err := store.HeadObject(context.TODO(), tt.key)
			if tt.wantedIsErr {
				assert.Contains(t, err.Error(), tt.wantedErrStr)
				assert.Nil(t, obj)
			} else {
				assert.Empty(t, err)
				assert.Equal(t, tt.wantedResult, obj.Key())
			}
		})
	}
}

func TestDiskFileStore_HeadDirSuccess(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name          string
		key           string
		offset        int64
		limit         int64
		wantedResult1 string
		wantedResult2 int64
		wantedErr     error
	}{
		{
			name:          "disk_file_head_dir__success_test1",
			limit:         6,
			key:           dir,
			wantedResult1: dir,
			wantedResult2: 0,
			wantedErr:     nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := setupDiskFileTest(t)
			obj, err := store.HeadObject(context.TODO(), tt.key)
			assert.Equal(t, tt.wantedErr, err)
			assert.Equal(t, tt.wantedResult1, obj.Key())
			assert.Equal(t, tt.wantedResult2, obj.Size())
		})
	}
}

func TestDiskFileStore_ListObjects(t *testing.T) {
	store := setupDiskFileTest(t)
	_, err := store.ListObjects(context.TODO(), emptyString, emptyString, emptyString, 0)
	assert.Equal(t, ErrUnsupportedMethod, err)
}

func TestDiskFileStore_ListAllObjects(t *testing.T) {
	store := setupDiskFileTest(t)
	_, err := store.ListAllObjects(context.TODO(), emptyString, emptyString)
	assert.Equal(t, ErrUnsupportedMethod, err)
}

func TestDiskFileStore_path(t *testing.T) {
	cases := []struct {
		name         string
		key          string
		wantedResult string
	}{
		{
			name:         "path_with_dir_suffix_test1",
			key:          mockKey,
			wantedResult: "test/mock",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := setupDiskFileTest(t)
			store.root = "test/"
			str := store.path(tt.key)
			assert.Equal(t, tt.wantedResult, str)
		})
	}
}

func TestDiskFileStore_PutObjectRoundtrip(t *testing.T) {
	store := &diskFileStore{root: t.TempDir() + "/"}

	err := store.PutObject(context.TODO(), "piece-key", strings.NewReader("piece-data"))
	assert.Nil(t, err)

	rc, err := store.GetObject(context.TODO(), "piece-key", 0, -1)
	assert.Nil(t, err)
	data, err := io.ReadAll(rc)
	assert.Nil(t, err)
	assert.Equal(t, "piece-data", string(data))

	// the temp file must be gone after a successful atomic rename
	leftovers, err := filepath.Glob(filepath.Join(store.root, ".piece-key.tmp*"))
	assert.Nil(t, err)
	assert.Empty(t, leftovers)
}

func TestDiskFileStore_PutObjectCreatesMissingDirectories(t *testing.T) {
	store := &diskFileStore{root: t.TempDir() + "/"}

	err := store.PutObject(context.TODO(), "a/b/piece-key", strings.NewReader("nested"))
	assert.Nil(t, err)

	rc, err := store.GetObject(context.TODO(), "a/b/piece-key", 0, -1)
	assert.Nil(t, err)
	data, err := io.ReadAll(rc)
	assert.Nil(t, err)
	assert.Equal(t, "nested", string(data))
}

func TestDiskFileStore_PutObjectOverwrites(t *testing.T) {
	store := &diskFileStore{root: t.TempDir() + "/"}

	assert.Nil(t, store.PutObject(context.TODO(), "piece-key", strings.NewReader("old")))
	assert.Nil(t, store.PutObject(context.TODO(), "piece-key", strings.NewReader("new-content")))

	rc, err := store.GetObject(context.TODO(), "piece-key", 0, -1)
	assert.Nil(t, err)
	data, err := io.ReadAll(rc)
	assert.Nil(t, err)
	assert.Equal(t, "new-content", string(data))
}
