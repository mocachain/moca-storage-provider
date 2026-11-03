package test

import (
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// every verify step must fail against empty tables
func TestVerifyStepsFailOnEmptyTables(t *testing.T) {
	for i, verify := range verifyFuncs {
		step := i + 1
		t.Run(fmt.Sprintf("verify%d", step), func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			for range 10 {
				mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{})
			require.NoError(t, err)

			assert.Error(t, verify(t, db), "verify%d passes against empty tables", step)
		})
	}
}
