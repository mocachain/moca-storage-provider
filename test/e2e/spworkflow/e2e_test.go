package spworkflow_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowScriptDoesNotHandleFundingPrivateKey(t *testing.T) {
	script, err := os.ReadFile("e2e_test.sh")
	require.NoError(t, err)
	require.NotContains(t, string(script), "FundingPrivateKey")
}
