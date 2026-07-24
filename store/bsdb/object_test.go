package bsdb

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/forbole/juno/v4/common"
	"github.com/stretchr/testify/assert"
)

func Test_GetObjectsTableName(t *testing.T) {
	objectTableName := GetObjectsTableName("ot005test-bucket")
	assert.Equal(t, "objects_62", objectTableName)
}

// ListObjectsByIDs backs an unauthenticated batch endpoint, so the per-object
// query must constrain results to publicly-readable objects.
func TestBsDBImpl_ListObjectsByIDs_FiltersToPublicRead(t *testing.T) {
	s, mock := setupDBRegexp(t)
	// The object id is first resolved to its bucket name.
	mock.ExpectQuery(`object_id_map`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket_name"}).AddRow("public-bucket"))
	// The per-object query must carry the visibility constraint.
	mock.ExpectQuery(`VISIBILITY_TYPE_PUBLIC_READ`).
		WillReturnRows(sqlmock.NewRows([]string{"object_name"}).AddRow("public-object"))

	objects, err := s.ListObjectsByIDs([]common.Hash{common.HexToHash("0x1")}, false, false)
	assert.NoError(t, err)
	assert.Len(t, objects, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Internal lookups (GetObjectByID for recovery/GC) pass includePrivate=true and
// must resolve private objects: no visibility constraint in the query.
func TestBsDBImpl_ListObjectsByIDs_IncludePrivateSkipsVisibilityFilter(t *testing.T) {
	s, mock := setupDBRegexp(t)
	mock.ExpectQuery(`object_id_map`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket_name"}).AddRow("private-bucket"))
	mock.ExpectQuery(`object_id = \? AND removed = \? LIMIT \?$`).
		WillReturnRows(sqlmock.NewRows([]string{"object_name"}).AddRow("private-object"))

	objects, err := s.ListObjectsByIDs([]common.Hash{common.HexToHash("0x1")}, false, true)
	assert.NoError(t, err)
	assert.Len(t, objects, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}
