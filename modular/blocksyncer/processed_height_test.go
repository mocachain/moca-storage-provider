package blocksyncer

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessedHeightConcurrentAccess(t *testing.T) {
	indexer := &Impl{}
	var writers sync.WaitGroup
	for writer := uint64(0); writer < 8; writer++ {
		writers.Add(1)
		go func(height uint64) {
			defer writers.Done()
			for i := 0; i < 1_000; i++ {
				indexer.setProcessedHeight(height)
				_ = indexer.processedHeight()
			}
		}(writer)
	}
	writers.Wait()

	require.Less(t, indexer.processedHeight(), uint64(8))
}
