package bsdb

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/forbole/juno/v4/common"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

const (
	mockGetBucketInfoByBucketNameQuerySQL = "SELECT * FROM `buckets` WHERE bucket_name = ? LIMIT ?"
)

// ListBucketsByIDs backs an unauthenticated batch endpoint, so the generated
// query must constrain results to publicly-readable buckets.
func TestBsDBImpl_ListBucketsByIDs_FiltersToPublicRead(t *testing.T) {
	s, mock := setupDBRegexp(t)
	mock.ExpectQuery(`bucket_id in \(\?\) AND visibility = \?`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket_name", "visibility"}).
			AddRow("public-bucket", "VISIBILITY_TYPE_PUBLIC_READ"))

	buckets, err := s.ListBucketsByIDs([]common.Hash{common.HexToHash("0x1")}, false, false)
	assert.NoError(t, err)
	assert.Len(t, buckets, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Internal callers (GVG migration, recovery, GC) pass includePrivate=true and
// must see private buckets: the query carries no visibility constraint.
// Regression guard for SP exit stalling on private buckets (moca-e2e #62).
func TestBsDBImpl_ListBucketsByIDs_IncludePrivateSkipsVisibilityFilter(t *testing.T) {
	s, mock := setupDBRegexp(t)
	mock.ExpectQuery(`bucket_id in \(\?\) AND removed = \?$`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket_name", "visibility"}).
			AddRow("private-bucket", "VISIBILITY_TYPE_PRIVATE"))

	buckets, err := s.ListBucketsByIDs([]common.Hash{common.HexToHash("0x1")}, false, true)
	assert.NoError(t, err)
	assert.Len(t, buckets, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// GetBucketMetaByName's public path must emit well-formed SQL. Regression guard
// for a missing space that produced "removed = false andbuckets.visibility".
func TestBsDBImpl_GetBucketMetaByName_PublicSQLWellFormed(t *testing.T) {
	s, mock := setupDBRegexp(t)
	mock.ExpectQuery(`removed = false and buckets.visibility=`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket_name"}).AddRow("public-bucket"))

	meta, err := s.GetBucketMetaByName("public-bucket", false)
	assert.NoError(t, err)
	assert.NotNil(t, meta)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestBsDBImpl_GetBucketInfoByBucketNameSuccess(t *testing.T) {
	expectedBucketName := "test-bucket"

	s, mock := setupDB(t)
	mock.ExpectQuery(mockGetBucketInfoByBucketNameQuerySQL).
		WithArgs(expectedBucketName, 1).
		WillReturnRows(
			sqlmock.NewRows([]string{"bucket_name"}).
				AddRow(expectedBucketName))

	bucket, err := s.GetBucketInfoByBucketName(expectedBucketName)
	assert.Nil(t, err)
	assert.Equal(t, expectedBucketName, bucket.BucketName)
}

func TestBsDBImpl_GetBucketInfoByBucketNameNoRecords(t *testing.T) {
	expectedBucketName := "test-bucket"
	s, mock := setupDB(t)
	mock.ExpectQuery(mockGetBucketInfoByBucketNameQuerySQL).WithArgs(expectedBucketName, 1).WillReturnError(gorm.ErrRecordNotFound)

	_, err := s.GetBucketInfoByBucketName(expectedBucketName)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

func TestBsDBImpl_GetBucketInfoByBucketNameDBError(t *testing.T) {
	expectedBucketName := "test-bucket"
	s, mock := setupDB(t)
	mock.ExpectQuery(mockGetBucketInfoByBucketNameQuerySQL).WithArgs(expectedBucketName, 1).WillReturnError(mockDBInternalError)

	_, err := s.GetBucketInfoByBucketName(expectedBucketName)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), mockDBInternalError.Error())
}
