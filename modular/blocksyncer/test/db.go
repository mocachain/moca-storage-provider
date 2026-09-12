package test

import (
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// RecreateDatabase drops and creates database name on the MySQL server at
// address so every run starts from an empty schema. The server has to exist
// already: "make test-mysql" starts one, or BLOCKSYNCER_TEST_DB_ADDRESS points
// at yours.
func RecreateDatabase(t testing.TB, user, password, address, name string) {
	t.Helper()

	db, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", user, password, address))
	if err != nil {
		t.Fatalf("failed to open the mysql server at %s: %v", address, err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err != nil {
		t.Fatalf("no mysql server at %s (start one with 'make test-mysql' or set BLOCKSYNCER_TEST_DB_ADDRESS): %v", address, err)
	}
	for _, statement := range []string{
		fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name),
		fmt.Sprintf("CREATE DATABASE `%s`", name),
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("failed to run %q on %s: %v", statement, address, err)
		}
	}
}
