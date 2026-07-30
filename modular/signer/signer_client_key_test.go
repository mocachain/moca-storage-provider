package signer

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The signing client used to keep every evm private key as a hex string in a
// map that lives for the whole process lifetime. Key material must only be
// retained in its parsed form, which the crypto libraries own and reuse.
func TestMocaChainSignClient_RetainsNoHexPrivateKeys(t *testing.T) {
	clientType := reflect.TypeOf(MocaChainSignClient{})
	stringMap := reflect.TypeOf(map[SignType]string{})

	for i := 0; i < clientType.NumField(); i++ {
		field := clientType.Field(i)
		assert.NotEqual(t, stringMap, field.Type,
			"field %s keeps private keys as hex strings", field.Name)
		assert.NotEqual(t, reflect.String, field.Type.Kind(),
			"field %s keeps key material as a string", field.Name)
	}
}

func TestCreateTxOpts_RejectsMissingPrivateKey(t *testing.T) {
	server := newGasPriceServer(t, "0x2a", false)
	client, err := ethclient.Dial(server.URL)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	var missing *ecdsa.PrivateKey
	txOpts, err := CreateTxOpts(context.Background(), client, missing, big.NewInt(5151), 700_000, 9)

	require.Error(t, err)
	assert.Nil(t, txOpts)
}
