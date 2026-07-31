package gfspconfig

import (
	"errors"
	"reflect"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"

	storeconfig "github.com/mocachain/moca-storage-provider/store/config"
)

var mockErr = errors.New("mock error")

func TestGfSpConfig_ApplySuccess(t *testing.T) {
	cfg := &GfSpConfig{Env: "mainnet"}
	opt := func(cfg *GfSpConfig) error { return nil }
	err := cfg.Apply(opt)
	assert.Equal(t, nil, err)
}

func TestGfSpConfig_ApplyFailure(t *testing.T) {
	cfg := &GfSpConfig{Env: "mainnet"}
	opt := func(cfg *GfSpConfig) error { return mockErr }
	err := cfg.Apply(opt)
	assert.Equal(t, mockErr, err)
}

func TestGfSpConfig_StringSuccess(t *testing.T) {
	cfg := &GfSpConfig{Env: "mainnet"}
	result := cfg.String()
	assert.NotNil(t, result)
}

func TestGfSpConfig_StringRedactsSecrets(t *testing.T) {
	cfg := &GfSpConfig{
		Env: "mainnet",
		SpAccount: SpAccountConfig{
			SpOperatorAddress:  "0xoperatoraddress",
			OperatorPrivateKey: "operator-private-key",
			FundingPrivateKey:  "funding-private-key",
			SealPrivateKey:     "seal-private-key",
			ApprovalPrivateKey: "approval-private-key",
			GcPrivateKey:       "gc-private-key",
			BlsPrivateKey:      "bls-private-key",
		},
		P2P:  P2PConfig{P2PPrivateKey: "p2p-private-key", P2PAddress: "127.0.0.1:9633"},
		SpDB: storeconfig.SQLDBConfig{User: "root", Passwd: "sp-db-password", Database: "sp"},
		BsDB: storeconfig.SQLDBConfig{User: "root", Passwd: "bs-db-password", Database: "bs"},
	}

	result := cfg.String()

	for _, secret := range []string{
		"operator-private-key", "funding-private-key", "seal-private-key",
		"approval-private-key", "gc-private-key", "bls-private-key",
		"p2p-private-key", "sp-db-password", "bs-db-password",
	} {
		assert.NotContains(t, result, secret)
	}
	// non secret fields are still visible for operability
	assert.Contains(t, result, "0xoperatoraddress")
	assert.Contains(t, result, "127.0.0.1:9633")
	assert.Contains(t, result, redactedValue)
	// the original config must not be mutated by rendering it
	assert.Equal(t, "operator-private-key", cfg.SpAccount.OperatorPrivateKey)
	assert.Equal(t, "sp-db-password", cfg.SpDB.Passwd)
}

func TestGfSpConfig_StringKeepsEmptySecretsEmpty(t *testing.T) {
	cfg := &GfSpConfig{Env: "mainnet"}

	result := cfg.String()

	assert.NotContains(t, result, redactedValue)
}

// secretFieldPattern matches config field names that hold credentials.
var secretFieldPattern = regexp.MustCompile(`(?i)(privatekey|passwd|password|secret|token|credential)`)

// TestGfSpConfig_StringRedactsEverySecretField walks the configuration, fills every
// field whose name says it holds a credential with a sentinel and asserts none of
// them survives rendering. It fails when a new secret field is added to the config
// without teaching String() about it.
func TestGfSpConfig_StringRedactsEverySecretField(t *testing.T) {
	const sentinel = "sentinel-secret-value"

	cfg := &GfSpConfig{}
	filled := fillSecretFields(t, reflect.ValueOf(cfg).Elem(), "", sentinel)
	assert.NotEmpty(t, filled, "the walk must find the known secret fields")
	assert.Contains(t, filled, "SpAccount.OperatorPrivateKey")
	assert.Contains(t, filled, "SpDB.Passwd")

	result := cfg.String()

	assert.NotContains(t, result, sentinel, "a secret field is rendered: %v", filled)
}

// fillSecretFields sets every settable string field with a secret looking name to
// value and returns the paths it filled.
func fillSecretFields(t *testing.T, v reflect.Value, prefix, value string) []string {
	t.Helper()
	var filled []string
	if v.Kind() != reflect.Struct {
		return filled
	}
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		path := field.Name
		if prefix != "" {
			path = prefix + "." + field.Name
		}
		fieldValue := v.Field(i)
		switch fieldValue.Kind() {
		case reflect.Struct:
			filled = append(filled, fillSecretFields(t, fieldValue, path, value)...)
		case reflect.String:
			if secretFieldPattern.MatchString(field.Name) {
				fieldValue.SetString(value)
				filled = append(filled, path)
			}
		}
	}
	return filled
}
