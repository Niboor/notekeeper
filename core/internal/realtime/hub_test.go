package realtime

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Niboor/notekeeper/core/internal/obs"
)

// SEC-API-4, NFR-O2: a user has at most MaxStreamsPerUser streams, and each one turned away is counted.
func TestStreamsOverTheLimitAreRefusedAndCounted(t *testing.T) {
	h := NewHub("", slog.Default())
	user, session := uuid.New(), uuid.New()
	before := testutil.ToFloat64(obs.StreamsRefused)
	for range MaxStreamsPerUser {
		if _, err := h.Subscribe(user, session); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Subscribe(user, session); !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("stream %d: %v, want ErrTooManyStreams", MaxStreamsPerUser+1, err)
	}
	if got := testutil.ToFloat64(obs.StreamsRefused) - before; got != 1 {
		t.Fatalf("refused streams counted: %v, want 1", got)
	}
	// Another user is not affected.
	if _, err := h.Subscribe(uuid.New(), session); err != nil {
		t.Fatal(err)
	}
}
