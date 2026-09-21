package deploycheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Grafana dashboards (deploy/grafana) show what the components export (NFR-O2): every query uses a metric that
// exists, so a panel cannot stay empty for ever because a name was mistyped, and the files import anywhere, because
// nothing in them is tied to one Grafana or one Prometheus.
func TestDashboardsUseRealMetricsAndImportAnywhere(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "grafana", "*.json"))
	if err != nil || len(files) < 4 {
		t.Fatalf("dashboards found: %d (%v)", len(files), err)
	}
	uids := map[string]string{}
	for _, f := range files {
		name := filepath.Base(f)
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d obj
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		uid, _ := d["uid"].(string)
		title, _ := d["title"].(string)
		if uid == "" || title == "" {
			t.Errorf("%s: a dashboard needs a uid and a title", name)
		}
		if other, dup := uids[uid]; dup {
			t.Errorf("%s: uid %q is also used by %s", name, uid, other)
		}
		uids[uid] = name
		checkMetricNames(t, name, string(raw))

		// The data source and the scrape jobs are chosen in the dashboard, never fixed in it.
		text := string(raw)
		if regexp.MustCompile(`"uid": "[^$"][^"]*"`).FindString(strings.ReplaceAll(text, `"uid": "`+uid+`"`, "")) != "" {
			t.Errorf("%s: a data source is fixed by its uid; use the ${datasource} variable", name)
		}
		if !strings.Contains(text, `"name": "datasource"`) {
			t.Errorf("%s: no datasource variable", name)
		}

		ids := map[float64]bool{}
		type box struct{ x, y, w, h float64 }
		var boxes []box
		panels, _ := d["panels"].([]any)
		queries := 0
		for _, p := range panels {
			panel, _ := p.(obj)
			id, _ := panel["id"].(float64)
			if ids[id] {
				t.Errorf("%s: panel id %v is used twice", name, id)
			}
			ids[id] = true
			pos, _ := panel["gridPos"].(obj)
			b := box{pos["x"].(float64), pos["y"].(float64), pos["w"].(float64), pos["h"].(float64)}
			if b.x < 0 || b.w <= 0 || b.x+b.w > 24 {
				t.Errorf("%s: panel %v is outside the 24 columns", name, panel["title"])
			}
			for _, o := range boxes {
				if b.x < o.x+o.w && o.x < b.x+b.w && b.y < o.y+o.h && o.y < b.y+b.h {
					t.Errorf("%s: panel %v overlaps another", name, panel["title"])
				}
			}
			boxes = append(boxes, b)
			if panel["type"] == "row" {
				continue
			}
			if s, _ := panel["title"].(string); s == "" {
				t.Errorf("%s: panel %v has no title", name, id)
			}
			if s, _ := panel["description"].(string); s == "" {
				t.Errorf("%s: panel %v has no description: say what it shows and what is bad", name, panel["title"])
			}
			ts, _ := panel["targets"].([]any)
			if len(ts) == 0 {
				t.Errorf("%s: panel %v has no query", name, panel["title"])
			}
			for _, q := range ts {
				expr, _ := q.(obj)["expr"].(string)
				if expr == "" {
					t.Errorf("%s: panel %v has an empty query", name, panel["title"])
				}
				queries++
			}
		}
		if queries < 10 {
			t.Errorf("%s: only %d queries", name, queries)
		}
	}
}
