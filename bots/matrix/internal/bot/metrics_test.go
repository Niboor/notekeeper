package bot

import (
	"io"
	"log/slog"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

func gather(t *testing.T, b *Bot) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := b.met.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, f := range families {
		out[f.GetName()] = f
	}
	return out
}

// NFR-O2: the bot serves what its own process is doing next to what it did for chats, says which version and
// whether it is the active replica, and measures how old messages are when it gets to them (the "bot lag").
func TestBotMetrics(t *testing.T) {
	b := &Bot{log: slog.New(slog.NewTextHandler(io.Discard, nil)), met: newMetrics()}
	b.RegisterBuildInfo("1.2.3")
	b.CoreRetried("post event")
	b.met.claimFailures.Inc()
	b.met.observeAge(time.Now().Add(-90 * time.Second))
	b.met.observeAge(time.Now().Add(time.Hour)) // a clock that is ahead is not a negative age

	got := gather(t, b)
	for _, name := range []string{"go_goroutines", "process_resident_memory_bytes", "nk_build_info", "nk_bot_core_retries_total",
		"nk_bot_outbox_claim_failures_total", "nk_bot_event_age_seconds", "nk_bot_leader"} {
		if got[name] == nil {
			t.Errorf("missing metric %s", name)
		}
	}
	if l := got["nk_build_info"].GetMetric()[0].GetLabel(); len(l) != 2 || l[0].GetValue() != "matrix-bot" || l[1].GetValue() != "1.2.3" {
		t.Errorf("build info labels: %v", l)
	}
	h := got["nk_bot_event_age_seconds"].GetMetric()[0].GetHistogram()
	if h.GetSampleCount() != 2 || h.GetSampleSum() < 89 || h.GetSampleSum() > 95 {
		t.Errorf("event age: %d samples, sum %v; want 2 samples of about 90 and 0 seconds", h.GetSampleCount(), h.GetSampleSum())
	}
	if v := got["nk_bot_leader"].GetMetric()[0].GetGauge().GetValue(); v != 0 {
		t.Errorf("leader before the lock = %v", v)
	}
	b.SetLeader(true)
	if v := gather(t, b)["nk_bot_leader"].GetMetric()[0].GetGauge().GetValue(); v != 1 {
		t.Errorf("leader after the lock = %v", v)
	}
}
