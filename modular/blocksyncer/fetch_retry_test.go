package blocksyncer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWaitForFetchRetryStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	retry := waitForFetchRetry(ctx)

	require.False(t, retry)
	require.Less(t, time.Since(start), 100*time.Millisecond)
}
