package gater

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSignedMsgAndSigAllowsCommaInSignedMessage(t *testing.T) {
	signedMsg, signature, err := parseSignedMsgAndSigFromRequest("Signature=0x1234,SignedMsg=sp-name,domain.example")

	require.NoError(t, err)
	require.Equal(t, "sp-name,domain.example", *signedMsg)
	require.Equal(t, "0x1234", *signature)
}
