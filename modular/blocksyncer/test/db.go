package test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// databasePrefixPattern keeps the generated name a plain identifier within MySQL's 64 bytes.
var databasePrefixPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,51}$`)

// CreateDatabase creates a database named prefix plus a random suffix on the
// MySQL server at address and drops that database again when the test ends, so
// a run never touches an existing schema or another run's. The server has to
// exist already: "make test-mysql" starts one, or BLOCKSYNCER_TEST_DB_ADDRESS
// points at yours. It returns the database name.
func CreateDatabase(t testing.TB, user, password, address, prefix string) string {
	t.Helper()

	if !databasePrefixPattern.MatchString(prefix) {
		t.Fatalf("invalid test database prefix %q: use letters, digits and underscores only", prefix)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("failed to pick a test database name: %v", err)
	}
	name := prefix + "_" + hex.EncodeToString(suffix)

	db, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", user, password, address))
	if err != nil {
		t.Fatalf("failed to open the mysql server at %s: %v", address, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("cannot use the mysql server at %s as %s (start one with 'make test-mysql', or set BLOCKSYNCER_TEST_DB_ADDRESS/_USER/_PASSWORD): %v", address, user, err)
	}
	// no IF NOT EXISTS: a name clash must fail rather than adopt somebody else's schema
	if _, err := db.Exec(fmt.Sprintf("CREATE DATABASE `%s`", name)); err != nil {
		_ = db.Close()
		t.Fatalf("failed to create the test database %s on %s: %v", name, address, err)
	}
	t.Cleanup(func() {
		defer func() { _ = db.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name)); err != nil {
			t.Logf("failed to drop the test database %s on %s: %v", name, address, err)
		}
	})
	return name
}
