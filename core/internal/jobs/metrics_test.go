package jobs

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Niboor/notekeeper/core/internal/obs"
)

// NFR-O2, SEC-AUD-3: every job run is counted with its result, so a failure ratio can be computed, and the
// time of the last success is kept, so a job that stopped running can be alerted on.
func TestFailedRecordsRunsDurationAndLastSuccess(t *testing.T) {
	job := "metrics_test_job"
	runs := func(result string) float64 { return testutil.ToFloat64(obs.JobRuns.WithLabelValues(job, result)) }

	if err := failed(job, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := failed(job, time.Now(), boom); !errors.Is(err, boom) {
		t.Fatalf("the error must be passed on, got %v", err)
	}
	if runs("ok") != 1 || runs("error") != 1 {
		t.Fatalf("runs: ok=%v error=%v, want 1 and 1", runs("ok"), runs("error"))
	}
	if got := testutil.ToFloat64(obs.JobFailures.WithLabelValues(job)); got != 1 {
		t.Fatalf("failures = %v, want 1", got)
	}
	last := testutil.ToFloat64(obs.JobLastSuccess.WithLabelValues(job))
	if time.Since(time.Unix(int64(last), 0)) > time.Minute {
		t.Fatalf("last success %v is not recent", last)
	}
	// A failing run does not move the time of the last success.
	before := last
	time.Sleep(1100 * time.Millisecond)
	_ = failed(job, time.Now(), boom)
	if got := testutil.ToFloat64(obs.JobLastSuccess.WithLabelValues(job)); got != before {
		t.Fatalf("a failure moved the last success from %v to %v", before, got)
	}
}
