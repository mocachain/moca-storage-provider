package sqldb

import (
	"os"
	"sync"
	"testing"

	"github.com/mocachain/moca-storage-provider/core/spdb"
	"github.com/stretchr/testify/require"
	mysqldialect "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// RECOVERY_TEST_MYSQL_DSN must name an empty disposable database. The test
// creates and drops integrity_meta_00; it refuses to use an existing table.
func TestRecoveryIntegrityMySQLPublishesWholeRows(t *testing.T) {
	dsn := os.Getenv("RECOVERY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("RECOVERY_TEST_MYSQL_DSN requires a disposable MySQL database")
	}
	db, err := gorm.Open(mysqldialect.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	require.NoError(t, db.Exec("CREATE TABLE integrity_meta_00 (object_id BIGINT UNSIGNED NOT NULL, redundancy_index INT NOT NULL, integrity_checksum TEXT, piece_checksum_list LONGTEXT, object_size BIGINT UNSIGNED, PRIMARY KEY(object_id,redundancy_index)) ENGINE=InnoDB").Error)
	t.Cleanup(func() { require.NoError(t, db.Exec("DROP TABLE integrity_meta_00").Error) })
	s := &SpDBImpl{db: db}
	a := &spdb.IntegrityMeta{ObjectID: 1, RedundancyIndex: -1, ObjectSize: 4, PieceChecksumList: [][]byte{[]byte("first")}, IntegrityChecksum: []byte("root-first")}
	b := &spdb.IntegrityMeta{ObjectID: 1, RedundancyIndex: -1, ObjectSize: 9, PieceChecksumList: [][]byte{[]byte("second"), []byte("third")}, IntegrityChecksum: []byte("root-second")}
	require.NoError(t, s.PublishRecoveryIntegrity(a))
	actual, err := s.GetObjectIntegrity(1, -1)
	require.NoError(t, err)
	require.Equal(t, a, actual)
	require.NoError(t, s.SetObjectIntegrity(b)) // Existing insert-only semantics stay unchanged.
	actual, err = s.GetObjectIntegrity(1, -1)
	require.NoError(t, err)
	require.Equal(t, a, actual)
	require.NoError(t, s.PublishRecoveryIntegrity(b))
	require.NoError(t, s.PublishRecoveryIntegrity(b))
	actual, err = s.GetObjectIntegrity(1, -1)
	require.NoError(t, err)
	require.Equal(t, b, actual)
	var workers sync.WaitGroup
	failures := make(chan error, 2)
	workers.Add(1)
	t.Cleanup(workers.Wait)
	go func() {
		defer workers.Done()
		for i := 0; i < 100; i++ {
			value := a
			if i%2 != 0 {
				value = b
			}
			if err := s.PublishRecoveryIntegrity(value); err != nil {
				failures <- err
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		actual, err = s.GetObjectIntegrity(1, -1)
		require.NoError(t, err)
		// Each row must be a complete published candidate, including size and order.
		if actual.ObjectSize == a.ObjectSize {
			require.Equal(t, a, actual)
		} else {
			require.Equal(t, b, actual)
		}
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
}
