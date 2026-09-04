package manager

import (
	"testing"
	"time"
)

func TestRetryBackoffDurationSaturates(t *testing.T) {
	if got := retryBackoffDuration(1); got != 4*time.Second {
		t.Fatalf("first retry backoff = %s, want 4s", got)
	}
	if got := retryBackoffDuration(61); got <= 0 {
		t.Fatalf("large retry backoff wrapped to %s", got)
	}
	if got := retryBackoffDuration(62); got != maxRetryBackoffDuration {
		t.Fatalf("overflowing retry backoff = %s, want saturation at %s", got, maxRetryBackoffDuration)
	}
	if got := retryBackoffDuration(1000); got != maxRetryBackoffDuration {
		t.Fatalf("very large retry backoff = %s, want saturation at %s", got, maxRetryBackoffDuration)
	}
}
