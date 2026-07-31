package utils

import (
	"flag"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
)

func TestMakeGfSpClientRejectsMissingGRPCTLS(t *testing.T) {
	client, err := MakeGfSpClient(&gfspconfig.GfSpConfig{})
	require.Nil(t, client)
	require.ErrorContains(t, err, "GRPCTLS.CACertFile is required")
}

// newTestContext builds a cli context with no flag explicitly set, which is what a
// production run that only supplies a config file looks like.
func newTestContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, f := range []cli.Flag{LogLevelFlag, LogPathFlag, LogStdOutputFlag} {
		require.NoError(t, f.Apply(set))
	}
	require.NoError(t, set.Parse(args))
	return cli.NewContext(cli.NewApp(), set, nil)
}

func TestInitLog_DefaultsToTheDocumentedFlagLevel(t *testing.T) {
	cfg := &gfspconfig.GfSpConfig{}

	require.NoError(t, initLog(newTestContext(t, "--log.std"), cfg))
	require.Equal(t, LogLevelFlag.Value, cfg.Log.Level,
		"an unset log level must fall back to the documented flag default, not to debug")
	require.NotEqual(t, "debug", cfg.Log.Level)
}

func TestInitLog_KeepsTheConfiguredLevel(t *testing.T) {
	cfg := &gfspconfig.GfSpConfig{}
	cfg.Log.Level = "warn"

	require.NoError(t, initLog(newTestContext(t, "--log.std"), cfg))
	require.Equal(t, "warn", cfg.Log.Level)
}

func TestInitLog_FlagOverridesTheConfiguredLevel(t *testing.T) {
	cfg := &gfspconfig.GfSpConfig{}
	cfg.Log.Level = "warn"

	require.NoError(t, initLog(newTestContext(t, "--log.std", "--log.level", "debug"), cfg))
	require.Equal(t, "debug", cfg.Log.Level)
}
