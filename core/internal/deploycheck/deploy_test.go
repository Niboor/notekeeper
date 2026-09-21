// Package deploycheck holds tests over the deployment files (images, nginx, example manifests). They
// check the security posture the requirements name, so an edit to a manifest that weakens it fails the
// build instead of surfacing in a cluster (SEC-OPS-*, NFR-D6).
package deploycheck

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"..", "..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type obj = map[string]any

func get(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(obj)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

// render builds the example overlay, or skips when the tool is not installed (`make tools`).
func render(t *testing.T) []obj {
	t.Helper()
	bin := filepath.Join("..", "..", "..", ".bin", "kustomize")
	if _, err := os.Stat(bin); err != nil {
		p, lookErr := exec.LookPath("kustomize")
		if lookErr != nil {
			t.Skip("kustomize is not installed (make tools)")
		}
		bin = p
	}
	out, err := exec.Command(bin, "build", filepath.Join("..", "..", "..", "deploy", "k8s", "overlays", "example")).Output()
	if err != nil {
		t.Fatalf("kustomize build: %v", err)
	}
	var docs []obj
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var d obj
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
	if len(docs) < 10 {
		t.Fatalf("only %d objects rendered", len(docs))
	}
	return docs
}

func podSpecs(docs []obj) map[string]obj {
	out := map[string]obj{}
	for _, d := range docs {
		switch d["kind"] {
		case "Deployment", "Job":
			out[get(d, "metadata", "name").(string)], _ = get(d, "spec", "template", "spec").(obj)
		}
	}
	return out
}

// Containers do not run as root or privileged, have read-only root filesystems, drop capabilities and
// cannot escalate (SEC-OPS-2).
// Also demonstrates: NFR-D3, NFR-D6.
func TestEveryPodIsLockedDown(t *testing.T) {
	for name, spec := range podSpecs(render(t)) {
		if get(spec, "securityContext", "runAsNonRoot") != true || get(spec, "securityContext", "seccompProfile", "type") != "RuntimeDefault" {
			t.Errorf("%s: pod security context is not restricted", name)
		}
		if get(spec, "automountServiceAccountToken") != false {
			t.Errorf("%s: mounts a service account token it does not need", name)
		}
		for _, c := range spec["containers"].([]any) {
			sc := get(c, "securityContext")
			if get(sc, "allowPrivilegeEscalation") != false || get(sc, "readOnlyRootFilesystem") != true || get(sc, "privileged") == true {
				t.Errorf("%s/%v: container security context is too open", name, get(c, "name"))
			}
			drop, _ := get(sc, "capabilities", "drop").([]any)
			if len(drop) != 1 || drop[0] != "ALL" || get(sc, "capabilities", "add") != nil {
				t.Errorf("%s/%v: capabilities are not dropped", name, get(c, "name"))
			}
			// Every writable volume must be writable by the user the pod runs as: a pod that runs as a
			// non-root user with memory volumes needs an fsGroup, or nginx starts with no server blocks.
			if vols, _ := spec["volumes"].([]any); len(vols) > 0 && get(spec, "securityContext", "fsGroup") == nil {
				t.Errorf("%s: has volumes but no fsGroup, so a non-root user may not be able to write them", name)
			}
			res := get(c, "resources", "limits", "memory")
			if res == nil {
				t.Errorf("%s/%v: no memory limit", name, get(c, "name"))
			}
		}
	}
}

// Nothing secret in a ConfigMap, only placeholders in the shipped Secrets, and no default credentials
// (SEC-OPS-1, SEC-OPS-7).
func TestSecretsAreReferencesAndPlaceholders(t *testing.T) {
	docs := render(t)
	secretish := regexp.MustCompile(`(?i)(password|secret|token|key|credential)`)
	for _, d := range docs {
		switch d["kind"] {
		case "ConfigMap":
			for k, v := range d["data"].(obj) {
				if secretish.MatchString(k) && !strings.HasSuffix(k, "_URL") {
					t.Errorf("ConfigMap key %s looks like a secret", k)
				}
				if strings.Contains(strings.ToLower(v.(string)), "change-me") {
					t.Errorf("ConfigMap value for %s is a placeholder", k)
				}
			}
		case "Secret":
			for k, v := range d["data"].(obj) { // Kustomize stores generated Secrets base64-encoded
				raw, err := base64.StdEncoding.DecodeString(v.(string))
				if err != nil || !strings.Contains(strings.ToLower(string(raw)), "change-me") {
					t.Errorf("Secret %v key %s ships a value that is not a placeholder", get(d, "metadata", "name"), k)
				}
			}
		}
	}
	// Every credential the components read comes from a Secret reference, never from inline env values.
	for name, spec := range podSpecs(docs) {
		for _, c := range spec["containers"].([]any) {
			if env, ok := get(c, "env").([]any); ok {
				for _, e := range env {
					if secretish.MatchString(get(e, "name").(string)) && get(e, "value") != nil {
						t.Errorf("%s: %v is set inline", name, get(e, "name"))
					}
				}
			}
		}
	}
}

// The schema owner's credential (SEC-OPS-5): it sits in a Secret of its own that only the migration Job
// reads. No serving pod, and no Secret a serving pod reads, holds it, and the Job reads nothing else.
func TestSchemaOwnerCredentialIsOnlyForTheMigrationJob(t *testing.T) {
	docs := render(t)
	holders := map[string]bool{} // Secrets that contain NK_MIGRATE_DATABASE_URL
	for _, d := range docs {
		if d["kind"] == "Secret" {
			if data, _ := d["data"].(obj); data != nil {
				if _, has := data["NK_MIGRATE_DATABASE_URL"]; has {
					holders[get(d, "metadata", "name").(string)] = true
				}
			}
		}
	}
	if len(holders) != 1 {
		t.Fatalf("the schema owner's URL must live in exactly one Secret, found in %v", holders)
	}
	secretsOf := func(spec obj) []string {
		var out []string
		for _, c := range spec["containers"].([]any) {
			ef, _ := get(c, "envFrom").([]any)
			for _, e := range ef {
				if n, ok := get(e, "secretRef", "name").(string); ok {
					out = append(out, n)
				}
			}
			env, _ := get(c, "env").([]any)
			for _, e := range env {
				if n, ok := get(e, "valueFrom", "secretKeyRef", "name").(string); ok {
					out = append(out, n)
				}
			}
		}
		return out
	}
	sawJob := false
	for name, spec := range podSpecs(docs) {
		isMigrate := strings.Contains(name, "migrate")
		for _, secret := range secretsOf(spec) {
			switch {
			case isMigrate && !holders[secret]:
				t.Errorf("%s reads Secret %s, which is not the schema owner's: the Job needs that one alone", name, secret)
			case !isMigrate && holders[secret]:
				t.Errorf("%s reads Secret %s, which holds the schema owner's credential", name, secret)
			}
		}
		sawJob = sawJob || isMigrate
	}
	if !sawJob {
		t.Fatal("no migration Job found")
	}
}

// Default deny, only the necessary paths, the bot API reachable by bots alone, and nothing routes to the
// bot API or the ops port from an ingress (SEC-OPS-3, SEC-OPS-4).
func TestNetworkPoliciesAndIngressRoutes(t *testing.T) {
	docs := render(t)
	denyAll := false
	var botIngress []any
	for _, d := range docs {
		if d["kind"] == "NetworkPolicy" {
			if sel, _ := get(d, "spec", "podSelector").(obj); len(sel) == 0 {
				types, _ := get(d, "spec", "policyTypes").([]any)
				if len(types) == 2 && get(d, "spec", "ingress") == nil && get(d, "spec", "egress") == nil {
					denyAll = true
				}
			}
			if get(d, "metadata", "name") == "core" {
				for _, rule := range get(d, "spec", "ingress").([]any) {
					for _, p := range get(rule, "ports").([]any) {
						if port := get(p, "port"); port == 8081 {
							botIngress = append(botIngress, rule)
						}
					}
				}
			}
		}
		if d["kind"] == "Ingress" {
			for _, rule := range get(d, "spec", "rules").([]any) {
				for _, p := range get(rule, "http", "paths").([]any) {
					svc := get(p, "backend", "service", "name")
					port := get(p, "backend", "service", "port", "number")
					if svc == "core-bot" || port == 8081 || port == 9090 {
						t.Errorf("the ingress routes to the bot or ops port: %v", p)
					}
				}
			}
			// The share host reaches only the public API on Core; everything else goes to the web image.
			for _, rule := range get(d, "spec", "rules").([]any) {
				if get(rule, "host") != "share.example.net" {
					continue
				}
				for _, p := range get(rule, "http", "paths").([]any) {
					if get(p, "backend", "service", "name") == "core-user" {
						t.Error("the share host routes to the user API")
					}
				}
			}
		}
	}
	if !denyAll {
		t.Error("there is no default-deny NetworkPolicy")
	}
	if len(botIngress) != 1 {
		t.Fatalf("the bot API must have exactly one allowed source, has %d", len(botIngress))
	}
	from := botIngress[0].(obj)["from"].([]any)
	if len(from) != 1 || get(from[0], "podSelector", "matchLabels", "app.kubernetes.io/name") != "matrix-bot" {
		t.Errorf("the bot API is open to more than the bot: %v", from)
	}
}

// TLS terminates at the ingress with modern protocols and HSTS (SEC-OPS-8, SEC-BASE-3, SEC-DATA-3).
func TestIngressEnforcesModernTLS(t *testing.T) {
	for _, d := range render(t) {
		if d["kind"] != "Ingress" {
			continue
		}
		a, _ := get(d, "metadata", "annotations").(obj)
		if a["nginx.ingress.kubernetes.io/ssl-redirect"] != "true" || a["nginx.ingress.kubernetes.io/hsts"] != "true" {
			t.Error("the ingress does not redirect to TLS or set HSTS")
		}
		protos, _ := a["nginx.ingress.kubernetes.io/ssl-protocols"].(string)
		if protos == "" || strings.Contains(protos, "TLSv1 ") || strings.Contains(protos, "TLSv1.1") || strings.HasPrefix(protos, "SSL") {
			t.Errorf("weak protocols allowed: %q", protos)
		}
		if tls, _ := get(d, "spec", "tls").([]any); len(tls) < 2 {
			t.Error("both hosts need a TLS entry")
		}
	}
}

// exportedMetrics is every metric name the components define, read from the source files that register them.
// A histogram also exports its _bucket, _sum and _count series.
func exportedMetrics(t *testing.T) map[string]bool {
	t.Helper()
	code := ""
	for _, f := range []string{"core/internal/obs/obs.go", "core/internal/obs/collectors.go", "core/internal/server/limits.go", "core/internal/httpx/middleware.go",
		"bots/matrix/internal/bot/metrics.go", "bots/matrix/internal/bot/bot.go"} {
		code += repoFile(t, filepath.FromSlash(f))
	}
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`"(nk_[a-z_]+)"`).FindAllStringSubmatch(code, -1) {
		names[m[1]] = true
	}
	return names
}

// checkMetricNames fails for every nk_ series in text that the exporters do not define.
func checkMetricNames(t *testing.T, what, text string) {
	t.Helper()
	exported := exportedMetrics(t)
	used := regexp.MustCompile(`\bnk_[a-z_]+`).FindAllString(text, -1)
	if len(used) < 8 {
		t.Fatalf("%s: only %d metrics referenced", what, len(used))
	}
	for _, m := range used {
		base := m
		for _, suffix := range []string{"_bucket", "_sum", "_count"} {
			if strings.HasSuffix(m, suffix) && exported[strings.TrimSuffix(m, suffix)] {
				base = strings.TrimSuffix(m, suffix)
			}
		}
		if !exported[base] {
			t.Errorf("%s uses %s, which nothing exports", what, m)
		}
	}
}

// The example alerts only refer to metrics that exist, so an alert cannot silently never fire (SEC-AUD-3).
func TestAlertsUseRealMetrics(t *testing.T) {
	rules := repoFile(t, "deploy", "k8s", "base", "prometheusrule.yaml")
	checkMetricNames(t, "an alert", rules)
	for _, need := range []string{"failed", "rejected", "not_found", "rate_limited"} {
		if !strings.Contains(rules, need) {
			t.Errorf("no alert for %s", need)
		}
	}
}

// Every label and annotation of an alert is a string with a plain name. A comma or colon inside an unquoted YAML flow
// mapping (`annotations: {summary: Many failures, which may be...}`) silently splits it into bogus keys with null values,
// which parses fine but is rejected by the Kubernetes API server (the CRD wants strings).
func TestAlertLabelsAndAnnotationsAreStrings(t *testing.T) {
	var doc obj
	if err := yaml.Unmarshal([]byte(repoFile(t, "deploy", "k8s", "base", "prometheusrule.yaml")), &doc); err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	groups, _ := get(doc, "spec", "groups").([]any)
	alerts := 0
	for _, g := range groups {
		rules, _ := get(g, "rules").([]any)
		for _, r := range rules {
			alert, _ := get(r, "alert").(string)
			if alert == "" {
				continue
			}
			alerts++
			if _, ok := get(r, "annotations", "summary").(string); !ok {
				t.Errorf("%s: annotations.summary is missing or not a string", alert)
			}
			for _, field := range []string{"labels", "annotations"} {
				m, ok := get(r, field).(obj)
				if !ok {
					t.Errorf("%s: %s is not a mapping", alert, field)
					continue
				}
				for k, v := range m {
					if !name.MatchString(k) {
						t.Errorf("%s: %s has the key %q; a comma in an unquoted value probably split the mapping (quote the value)", alert, field, k)
					}
					if _, ok := v.(string); !ok {
						t.Errorf("%s: %s.%s is %T, not a string (quote the value)", alert, field, k, v)
					}
				}
			}
		}
	}
	if alerts < 10 {
		t.Fatalf("only %d alerts found", alerts)
	}
}

// The "is it running" alerts match the scrape job by name. An unanchored `.*core.*` also matches coredns on a
// kube-prometheus-stack cluster, so absent() could never fire; the pattern must not match other components.
func TestUpAlertsDoNotMatchOtherJobs(t *testing.T) {
	rules := repoFile(t, "deploy", "k8s", "base", "prometheusrule.yaml")
	for alert, want := range map[string][]string{
		"NotekeeperCoreDown": {"core", "core-metrics", "notekeeper/core", "notekeeper-core"},
		"NotekeeperBotDown":  {"matrix-bot", "matrix-bot-metrics", "notekeeper/matrix-bot", "notekeeper-matrix-bot"},
	} {
		expr := regexp.MustCompile(`(?s)alert: ` + alert + `\n\s+expr: absent\(up\{job=~"([^"]+)"\} == 1\)`).FindStringSubmatch(rules)
		if expr == nil {
			t.Fatalf("%s: no absent(up{job=~\"...\"}) expression found", alert)
		}
		// Prometheus anchors label regexes on both ends.
		re, err := regexp.Compile("^(?:" + expr[1] + ")$")
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range want {
			if !re.MatchString(job) {
				t.Errorf("%s: pattern %q does not match the job %q", alert, expr[1], job)
			}
		}
		for _, job := range []string{"coredns", "kube-dns", "apiserver", "kubelet", "notekeeper-db", "synapse", "matrix-bridge"} {
			if re.MatchString(job) {
				t.Errorf("%s: pattern %q also matches the unrelated job %q", alert, expr[1], job)
			}
		}
	}
}

// The images run as non-root, from minimal bases with pinned major versions, and carry no secrets (SEC-OPS-1, SEC-OPS-2, SEC-OPS-6).
func TestImagesAreMinimalAndNonRoot(t *testing.T) {
	for _, f := range []string{"Dockerfile.core", "Dockerfile.bot", "Dockerfile.web"} {
		text := repoFile(t, "deploy", "docker", f)
		froms := regexp.MustCompile(`(?m)^FROM ([^ ]+)`).FindAllStringSubmatch(text, -1)
		if len(froms) < 2 {
			t.Errorf("%s: not a multi-stage build", f)
		}
		for _, m := range froms {
			if strings.HasSuffix(m[1], ":latest") || !strings.Contains(m[1], ":") {
				t.Errorf("%s: base image %s is not pinned", f, m[1])
			}
		}
		last := froms[len(froms)-1][1]
		if minimal := strings.Contains(last, "distroless") || strings.Contains(last, "unprivileged") || strings.Contains(last, "slim"); !minimal {
			t.Errorf("%s: final image %s is not a minimal base", f, last)
		}
		if !regexp.MustCompile(`(?m)^USER (65532|101)`).MatchString(text) {
			t.Errorf("%s: does not switch to a non-root user", f)
		}
		if regexp.MustCompile(`(?im)^(ENV|ARG) .*(PASSWORD|SECRET|TOKEN|KEY)=`).MatchString(text) {
			t.Errorf("%s: a secret in the image definition", f)
		}
	}
}

// The share host serves only the share page and its API, and drops cookies both ways (CORE-SH8, CORE-SH10, SEC-SHR-5, SEC-SHR-6).
func TestShareHostServerBlockIsMinimal(t *testing.T) {
	text := repoFile(t, "deploy", "nginx", "share.conf.template")
	for _, need := range []string{`proxy_set_header Cookie ""`, "proxy_hide_header Set-Cookie", "noindex", "location / { return 404; }", "location = /s"} {
		if !strings.Contains(text, need) {
			t.Errorf("the share server block lacks %q", need)
		}
	}
	if strings.Contains(text, "/api/v1") || strings.Contains(text, "try_files $uri /index.html") {
		t.Error("the share host must not serve the application or its API")
	}
	headers := repoFile(t, "deploy", "nginx", "headers.conf")
	if !strings.Contains(headers, "script-src 'self'") || strings.Contains(headers, "unsafe-eval") || regexp.MustCompile(`script-src[^;]*unsafe-inline`).MatchString(headers) {
		t.Error("the content security policy allows inline or eval scripts")
	}
}

// The application host never serves the share page, so a share token is never handled on the app's origin (CORE-SH8, SEC-SHR-5).
func TestAppHostRefusesTheSharePage(t *testing.T) {
	text := repoFile(t, "deploy", "nginx", "app.conf.template")
	block := regexp.MustCompile(`location ~ \^/s\(/\|\$\) \{ return 404; \}`).FindStringIndex(text)
	if block == nil {
		t.Fatal("the app server block does not answer 404 for /s and below")
	}
	if fallback := strings.Index(text, "try_files $uri /index.html"); fallback < 0 || block[0] > fallback {
		t.Error("the /s block must come before the single-page fallback")
	}
}

// Database connections in the example manifests require TLS (SEC-DATA-4), and nothing in the repository
// parses or transcodes untrusted media: files are stored and served as they came (SEC-CNT-5, CORE-A4).
func TestDatabaseTLSAndNoMediaProcessing(t *testing.T) {
	overlay := repoFile(t, "deploy", "k8s", "overlays", "example", "kustomization.yaml")
	for _, line := range strings.Split(overlay, "\n") {
		if strings.Contains(line, "DATABASE_URL=") && !strings.Contains(line, "sslmode=verify-full") {
			t.Errorf("a database URL without TLS: %s", strings.TrimSpace(line))
		}
	}
	mods := repoFile(t, "core", "go.mod") + repoFile(t, "bots", "matrix", "go.mod") + repoFile(t, "bots", "sdk", "go.mod")
	for _, lib := range []string{"disintegration/imaging", "nfnt/resize", "h2non/bimg", "davidbyttow/govips", "pdfcpu", "unidoc", "ffmpeg", "gographics/imagick", "golang.org/x/image", "chai2010/webp"} {
		if strings.Contains(mods, lib) {
			t.Errorf("a media processing library is a dependency: %s", lib)
		}
	}
	_ = filepath.WalkDir(filepath.Join("..", "..", ".."), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "node_modules") {
			return nil
		}
		b, _ := os.ReadFile(path)
		if regexp.MustCompile(`(?m)^\s*"image(/[a-z]+)?"$`).Match(b) {
			t.Errorf("%s imports the image package: uploads must not be decoded", path)
		}
		return nil
	})
}

// The workflows do what the setup promises: the full CI runs on pull requests and on merges to master, and a
// version tag publishes the three images to ghcr only after that CI has passed, with write access to
// packages given to the publishing job alone (SEC-OPS-6).
func TestWorkflowsForPullRequestsMergesAndReleases(t *testing.T) {
	load := func(name string) obj {
		var d obj
		if err := yaml.Unmarshal([]byte(repoFile(t, ".github", "workflows", name)), &d); err != nil {
			t.Fatal(name, err)
		}
		return d
	}
	ci, release := load("ci.yml"), load("release.yml")
	// yaml.v3 reads the key `on` as a string.
	triggers, _ := ci["on"].(obj)
	for _, want := range []string{"pull_request", "push", "workflow_call"} {
		if _, ok := triggers[want]; !ok {
			t.Errorf("ci.yml is not triggered by %s", want)
		}
	}
	if branches, _ := get(triggers, "push", "branches").([]any); len(branches) == 0 {
		t.Error("ci.yml does not run on pushes to the main branch")
	}
	jobs, _ := get(ci, "jobs").(obj)
	for _, job := range []string{"generate", "lint", "unit", "integration-core", "integration-bot", "security", "e2e", "e2e-cross-browser", "images", "e2e-stack", "passed"} {
		if get(ci, "jobs", job) == nil {
			t.Errorf("ci.yml has no %s job", job)
		}
	}
	// "CI passed" is the one status to require: it must wait for every other job, or a job added later could fail unnoticed.
	waits := map[string]bool{}
	for _, n := range get(ci, "jobs", "passed", "needs").([]any) {
		waits[n.(string)] = true
	}
	for job := range jobs {
		if job != "passed" && !waits[job] {
			t.Errorf("the passed job does not wait for %s", job)
		}
	}
	// Everything `make check` runs also runs in CI, in some job, so that splitting the check into jobs drops nothing.
	ciText := repoFile(t, ".github", "workflows", "ci.yml")
	makefile := repoFile(t, "Makefile")
	checkLine := regexp.MustCompile(`(?m)^check:\s*([^#\n]*)`).FindStringSubmatch(makefile)
	if checkLine == nil {
		t.Fatal("the Makefile has no check target")
	}
	for _, target := range strings.Fields(checkLine[1]) {
		wanted := []string{target}
		if target == "test-integration" {
			wanted = []string{"test-integration-core", "test-integration-bot"} // run side by side in CI
		}
		for _, w := range wanted {
			if !regexp.MustCompile(`(?m)run:\s*make\b[^\n]*\b` + regexp.QuoteMeta(w) + `\b`).MatchString(ciText) {
				t.Errorf("`make check` runs %s, but no job of ci.yml does", w)
			}
		}
	}
	rt, _ := release["on"].(obj)
	tags, _ := get(rt, "push", "tags").([]any)
	if len(tags) == 0 || !strings.HasPrefix(tags[0].(string), "v") {
		t.Errorf("release.yml is not triggered by version tags: %v", tags)
	}
	if get(release, "jobs", "ci", "uses") != "./.github/workflows/ci.yml" {
		t.Error("the release does not run the CI workflow first")
	}
	// Publishing waits for the tests and for the gate that says whether they can be skipped (the commit already
	// passed them on master); it runs only when they passed here or were skipped for that reason.
	needs := map[string]bool{}
	for _, n := range get(release, "jobs", "publish", "needs").([]any) {
		needs[n.(string)] = true
	}
	if !needs["ci"] || !needs["gate"] {
		t.Errorf("publishing does not wait for the tests and the gate: needs = %v", needs)
	}
	condition, _ := get(release, "jobs", "publish", "if").(string)
	if !strings.Contains(condition, "needs.ci.result == 'success'") || !strings.Contains(condition, "needs.gate.outputs.passed == 'true'") {
		t.Errorf("publishing is not tied to the tests having passed: if = %q", condition)
	}
	if get(release, "jobs", "ci", "if") == nil {
		t.Error("the release runs the tests even when the commit has already passed them")
	}
	if get(release, "permissions", "packages") != nil {
		t.Error("write access to packages is granted to the whole release workflow, not only to publishing")
	}
	if get(release, "jobs", "publish", "permissions", "packages") != "write" {
		t.Error("the publishing job cannot push to ghcr")
	}
	text := repoFile(t, ".github", "workflows", "release.yml")
	for _, image := range []string{"notekeeper-${{ matrix.image }}", "Dockerfile.core", "Dockerfile.web", "Dockerfile.bot", "ghcr.io"} {
		if !strings.Contains(text, image) {
			t.Errorf("release.yml does not mention %s", image)
		}
	}
}
