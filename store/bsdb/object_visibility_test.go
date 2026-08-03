package bsdb

import (
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListObjectsByBucketName_PublicOnlyQueryFiltersVisibility pins that a listing for
// a caller that does not own the bucket is restricted at the query level. Without the
// predicate the statement returns every row of the bucket, private objects included.
func TestListObjectsByBucketName_PublicOnlyQueryFiltersVisibility(t *testing.T) {
	s, mock := setupDB(t)

	mock.ExpectQuery(fmt.Sprintf("SELECT * FROM `%s` WHERE (bucket_name = ? and removed = false) "+
		"AND (visibility = 'VISIBILITY_TYPE_PUBLIC_READ' or "+
		"(visibility = 'VISIBILITY_TYPE_INHERIT' and exists "+
		"(select 1 from buckets where buckets.bucket_name = ? and buckets.visibility = 'VISIBILITY_TYPE_PUBLIC_READ'))) "+
		"ORDER BY object_name asc LIMIT ?", GetObjectsTableName(bucketName))).
		WithArgs(bucketName, bucketName, 3).
		WillReturnRows(sqlmock.NewRows([]string{"object_id", "object_name"}))

	_, err := s.ListObjectsByBucketName(bucketName, "", "", "", 2, false, false)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestListObjectsByBucketName_OwnerQueryKeepsEveryObject is the counterpart: the owner
// gets the unfiltered statement.
func TestListObjectsByBucketName_OwnerQueryKeepsEveryObject(t *testing.T) {
	s, mock := setupDB(t)

	mock.ExpectQuery(fmt.Sprintf("SELECT * FROM `%s` WHERE bucket_name = ? and removed = false "+
		"ORDER BY object_name asc LIMIT ?", GetObjectsTableName(bucketName))).
		WithArgs(bucketName, 3).
		WillReturnRows(sqlmock.NewRows([]string{"object_id", "object_name"}))

	_, err := s.ListObjectsByBucketName(bucketName, "", "", "", 2, false, true)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
