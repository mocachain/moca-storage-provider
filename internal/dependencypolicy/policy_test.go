package dependencypolicy

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateModulesKeepChecksumVerificationEnabled(t *testing.T) {
	dockerfile, err := os.ReadFile("../../Dockerfile")
	require.NoError(t, err)
	require.NotContains(t, string(dockerfile), "GONOSUMDB=github.com/mocachain/*")
	require.NotContains(t, string(dockerfile), "GONOSUMCHECK=github.com/mocachain/*")
}

func TestPrivateModuleReplacementsAreNotPinnedToPrereleaseTags(t *testing.T) {
	goMod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)

	floatingPrerelease := regexp.MustCompile(`github\.com/mocachain/[^\s]+\s+v[^\s]+-rc\d+`)
	require.Empty(t, floatingPrerelease.FindAllString(string(goMod), -1))
}
