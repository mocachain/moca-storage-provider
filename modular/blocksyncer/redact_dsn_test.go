package blocksyncer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactDSNMasksThePassword(t *testing.T) {
	const password = "p4ss?{w0rd}<>#%"
	dsn := "devnet2admin:" + password + "@tcp(db.internal:3306)/storage_provider_0_block_syncer?parseTime=true&multiStatements=true&loc=Local&interpolateParams=true"

	got := redactDSN(dsn)

	require.NotContains(t, got, password)
	require.Contains(t, got, "devnet2admin:***@tcp(db.internal:3306)/storage_provider_0_block_syncer")
	require.Contains(t, got, "multiStatements=true")

	// A DSN the driver cannot parse is not echoed back at all.
	require.Equal(t, "<unparseable dsn>", redactDSN("devnet2admin:secret@tcp(db.internal:3306)"))
}
