package executor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/base/types/gfsperrors"
)

// The gfsperrors registry is keyed by inner code alone and the last
// registration wins, so a duplicated code silently aliases one error to
// another. Resolving every declared error back through the registry proves
// each code is unique among everything registered in this binary.
func TestDeclaredErrorCodesResolveToThemselves(t *testing.T) {
	registered := make(map[int32]*gfsperrors.GfSpError)
	for _, e := range gfsperrors.GfSpErrorList() {
		registered[e.GetInnerCode()] = e
	}
	for _, declared := range []*gfsperrors.GfSpError{
		ErrDanglingPointer,
		ErrInsufficientApproval,
		ErrUnsealed,
		ErrExhaustedApproval,
		ErrInvalidIntegrity,
		ErrSecondaryMismatch,
		ErrReplicateIdsOutOfBounds,
		ErrRecoveryRedundancyType,
		ErrRecoveryPieceNotEnough,
		ErrRecoveryDecode,
		ErrRecoveryPieceChecksum,
		ErrRecoveryPieceLength,
		ErrPrimaryNotFound,
		ErrRecoveryPieceIndex,
		ErrMigratedPieceChecksum,
		ErrInvalidRedundancyIndex,
		ErrSetObjectIntegrity,
		ErrInvalidPieceChecksumLength,
		ErrRecoveryObjectStatus,
		ErrInvalidSecondaryBlsSignature,
		ErrRecoveryChainChecksum,
		ErrInvalidReplicatePieceTask,
	} {
		got, ok := registered[declared.GetInnerCode()]
		require.Truef(t, ok, "inner code %d is not registered", declared.GetInnerCode())
		assert.Samef(t, declared, got, "inner code %d resolves to %q instead of %q",
			declared.GetInnerCode(), got.GetDescription(), declared.GetDescription())
	}
}
