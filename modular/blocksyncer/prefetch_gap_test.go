package blocksyncer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrefetchTooFarAheadBoundsTheBacklogAfterRestart(t *testing.T) {
	const workers = 50
	limit := uint64(MaxHeightGapFactor * workers)

	// A fresh process exports nothing until the first block after the epoch succeeds.
	require.False(t, prefetchTooFarAhead(1, 0, workers))
	require.False(t, prefetchTooFarAhead(limit, 0, workers))
	require.True(t, prefetchTooFarAhead(limit+1, 0, workers))

	// Resuming from an epoch seeds the processed height and allows exactly the gap window.
	const epoch = uint64(25_424_796)
	require.False(t, prefetchTooFarAhead(epoch+1, epoch, workers))
	require.False(t, prefetchTooFarAhead(epoch+limit, epoch, workers))
	require.True(t, prefetchTooFarAhead(epoch+limit+1, epoch, workers))

	// The fetcher is never throttled while it trails the exporter.
	require.False(t, prefetchTooFarAhead(10, 20, workers))
}
