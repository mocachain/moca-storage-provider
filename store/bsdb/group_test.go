package bsdb

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/forbole/juno/v4/common"
	"github.com/stretchr/testify/require"
)

func TestListGroupsByNameAndSourceTypeEscapesLikeMetacharacters(t *testing.T) {
	db, mock := setupDB(t)
	pattern := `team\%%\_ops%`
	address := common.HexToAddress(GroupAddress)

	mock.ExpectQuery("SELECT * FROM `groups` WHERE (group_name LIKE ? ESCAPE '\\\\' and account_id = ?) AND removed = ? ORDER BY group_id LIMIT ?").
		WithArgs(pattern, address, false, 10).
		WillReturnRows(sqlmock.NewRows([]string{}))
	mock.ExpectQuery("SELECT count(*) FROM `groups` WHERE (group_name LIKE ? ESCAPE '\\\\' and account_id = ?) AND removed = ? LIMIT ?").
		WithArgs(pattern, address, false, 1).
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))

	_, _, err := db.ListGroupsByNameAndSourceType("_ops", "team%", "", 10, 0, false)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
