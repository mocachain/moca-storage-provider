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

func (s *BasicTestSuite) Test_BlockSyncer() {
	go test.MockChainRPCServer()
	s.Require().NoError(test.WaitForMockChainRPCServer(5 * time.Second))

	args := []string{"", "-config", "config.toml", "--server", "blocksyncer"}

	go func() {
		if err := App.Run(args); err != nil {
			log.Error(err)
		}
	}()

	time.Sleep(time.Second * 20)

	err := test.Verify(s.T())
	s.Equal(nil, err)
}

func TestBasicTestSuite(t *testing.T) {
	suite.Run(t, new(BasicTestSuite))
}
