package signer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateTxOptsSignsWithTheScopeKey pins the invariant that an evm transaction
// is signed with the key of the same account the message sender, chain id and
// nonce were taken from. Every one of those is derived from the scope parameter,
// so the signing key has to be indexed by scope too. Hardcoding a SignType there
// makes the signer send a transaction from one account with another account's
// nonce.
func TestCreateTxOptsSignsWithTheScopeKey(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "signer_client.go", nil, 0)
	require.NoError(t, err)

	calls := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "CreateTxOpts" {
			return true
		}
		calls++
		require.Greater(t, len(call.Args), 3, "unexpected CreateTxOpts signature")

		key := call.Args[2]
		index, ok := key.(*ast.IndexExpr)
		if !ok {
			t.Errorf("%s: CreateTxOpts signs with %s, want the key map indexed by scope",
				fset.Position(call.Pos()), types.ExprString(key))
			return true
		}
		if scope, ok := index.Index.(*ast.Ident); !ok || scope.Name != "scope" {
			t.Errorf("%s: CreateTxOpts signs with %s, but the sender, chain id and nonce come from scope",
				fset.Position(call.Pos()), types.ExprString(key))
		}
		return true
	})
	assert.NotZero(t, calls, "no CreateTxOpts call found, the guard would pass vacuously")
}
