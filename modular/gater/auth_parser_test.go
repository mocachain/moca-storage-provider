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

func TestParseSignedMsgAndSigAllowsSignedMessageFirst(t *testing.T) {
	signedMsg, signature, err := parseSignedMsgAndSigFromRequest("SignedMsg=sp-name,domain.example,Signature=0x1234")

	require.NoError(t, err)
	require.Equal(t, "sp-name,domain.example", *signedMsg)
	require.Equal(t, "0x1234", *signature)
}

func TestParseSignedMsgAndSigRejectsDuplicateFields(t *testing.T) {
	_, _, err := parseSignedMsgAndSigFromRequest("Signature=0x1234,SignedMsg=message,Signature=0xabcd")

	require.ErrorIs(t, err, ErrAuthorizationHeaderFormat)
}
