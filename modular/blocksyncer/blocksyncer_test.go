package blocksyncer

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v2"

	"github.com/mocachain/moca-storage-provider/cmd/utils"
	"github.com/mocachain/moca-storage-provider/modular/blocksyncer/test"
	"github.com/mocachain/moca-storage-provider/pkg/log"
)

func TestStorageProviderRejectsLegacyFundingPrivateKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[SpAccount]\nFundingPrivateKey = 'legacy-key'\n"), 0o600))

	set := flag.NewFlagSet("test", flag.ContinueOnError)
	require.NoError(t, utils.ConfigFileFlag.Apply(set))
	require.NoError(t, set.Parse([]string{"--config", configPath}))
	err := StorageProvider(cli.NewContext(cli.NewApp(), set, nil))

	require.ErrorContains(t, err, "FundingPrivateKey is not supported")
}

type BasicTestSuite struct {
	BlockSyncerE2eBaseSuite
}

func (s *BasicTestSuite) SetupSuite() {
	s.BlockSyncerE2eBaseSuite.SetupSuite()
}

// Test_BlockSyncer runs the blocksyncer against the mock chain and a MySQL
// server, recreating its database first so every run starts from an empty
// schema, then verifies what it wrote. The server defaults to 127.0.0.1:3306
// with root/root, which "make test-mysql" provides; the BLOCKSYNCER_TEST_DB_*
// variables (exported by modular/blocksyncer/test/bs_test.sh) override it.
func (s *BasicTestSuite) Test_BlockSyncer() {
	dir := s.T().TempDir()
	stack := test.StackConfig{
		DBUser:     envOrDefault("BLOCKSYNCER_TEST_DB_USER", "root"),
		DBPassword: envOrDefault("BLOCKSYNCER_TEST_DB_PASSWORD", "root"),
		DBAddress:  envOrDefault("BLOCKSYNCER_TEST_DB_ADDRESS", "127.0.0.1:3306"),
		DBName:     envOrDefault("BLOCKSYNCER_TEST_DB_NAME", "block_syncer"),
	}
	test.RecreateDatabase(s.T(), stack.DBUser, stack.DBPassword, stack.DBAddress, stack.DBName)

	go test.MockChainRPCServerAt("127.0.0.1:0")
	s.Require().NoError(test.WaitForMockChainRPCServer(5 * time.Second))
	stack.ChainAddress = test.MockChainAddress()

	configPath := test.WriteConfig(s.T(), dir, stack)
	args := []string{"", "-config", configPath, "--server", "blocksyncer"}

	go func() {
		if err := App.Run(args); err != nil {
			log.Error(err)
		}
	}()

	time.Sleep(time.Second * 20)

	err := test.Verify(s.T())
	s.Equal(nil, err)
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func TestBasicTestSuite(t *testing.T) {
	suite.Run(t, new(BasicTestSuite))
}
