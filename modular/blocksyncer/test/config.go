package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	coremodule "github.com/mocachain/moca-storage-provider/core/module"
	storeconfig "github.com/mocachain/moca-storage-provider/store/config"
)

// StackConfig describes the database and mock chain a blocksyncer under test talks to.
type StackConfig struct {
	DBUser       string
	DBPassword   string
	DBAddress    string
	DBName       string
	ChainAddress string
}

// WriteConfig writes a blocksyncer-only config.toml into dir, with the settings
// modular/blocksyncer/test/bs_test.sh used to patch into a config.dump, and
// returns its path.
func WriteConfig(t testing.TB, dir string, stack StackConfig) string {
	t.Helper()

	caFile, certFile, keyFile := WriteTestTLS(t, dir, "blocksyncer")
	db := storeconfig.SQLDBConfig{
		User:     stack.DBUser,
		Passwd:   stack.DBPassword,
		Address:  stack.DBAddress,
		Database: stack.DBName,
	}

	cfg := gfspconfig.DefaultConfig()
	cfg.Server = []string{coremodule.BlockSyncerModularName}
	cfg.GRPCTLS = gfspconfig.GRPCTLSConfig{CACertFile: caFile, CertFile: certFile, KeyFile: keyFile}
	cfg.SpDB = db
	cfg.BsDB = db
	cfg.Chain.ChainID = "moca_5151-1"
	cfg.Chain.ChainAddress = []string{"http://" + stack.ChainAddress}
	cfg.Chain.RpcAddress = []string{"http://127.0.0.1:8545"}
	cfg.Log.Path = filepath.Join(dir, "blocksyncer.log")
	cfg.Monitor.DisableMetrics = true
	cfg.Monitor.DisablePProf = true
	// the base app dereferences the probe unconditionally, so keep it on a free port
	cfg.Monitor.ProbeHTTPAddress = "127.0.0.1:0"
	cfg.BlockSyncer = gfspconfig.BlockSyncerConfig{
		Modules: []string{
			"epoch", "bucket", "object", "payment", "group", "permission", "storage_provider",
			"prefix_tree", "virtual_group", "sp_exit_events", "object_id_map", "general",
		},
		Workers:                10,
		DataMonitor:            true,
		DataStatisticsDuration: 5,
		ChainDataStorage:       gfspconfig.ChainDataStorage{EnableStorage: true, MaximumStorageCount: 50},
	}

	bz, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("failed to marshal the test config: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, bz, 0o600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}
