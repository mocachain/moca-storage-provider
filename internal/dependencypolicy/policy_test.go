package dependencypolicy

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMocachainModulesKeepChecksumVerificationEnabled(t *testing.T) {
	dockerfile, err := os.ReadFile("../../Dockerfile")
	require.NoError(t, err)
	require.NotContains(t, string(dockerfile), "GOPRIVATE=github.com/mocachain")
	require.NotContains(t, string(dockerfile), "GONOSUMDB=github.com/mocachain/*")
	require.NotContains(t, string(dockerfile), "GONOSUMCHECK=github.com/mocachain/*")
}
