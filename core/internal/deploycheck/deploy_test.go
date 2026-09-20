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

// The example alerts only refer to metrics that exist, so an alert cannot silently never fire (SEC-AUD-3).
func TestAlertsUseRealMetrics(t *testing.T) {
	rules := repoFile(t, "deploy", "k8s", "base", "prometheusrule.yaml")
	code := ""
	for _, f := range []string{"core/internal/obs/obs.go", "core/internal/obs/collectors.go", "core/internal/server/limits.go"} {
		code += repoFile(t, filepath.FromSlash(f))
	}
	exprs := regexp.MustCompile(`\bnk_[a-z_]+`).FindAllString(rules, -1)
	if len(exprs) < 8 {
		t.Fatalf("only %d metrics referenced", len(exprs))
	}
	for _, m := range exprs {
		base := strings.TrimSuffix(strings.TrimSuffix(m, "_bucket"), "_total")
		if !strings.Contains(code, `"`+m+`"`) && !strings.Contains(code, `"`+base+`"`) && !strings.Contains(code, `"`+base+`_total"`) && !strings.Contains(code, `"`+base+`_seconds"`) {
			t.Errorf("an alert uses %s, which nothing exports", m)
		}
	}
	for _, need := range []string{"failed", "rejected", "not_found", "rate_limited"} {
		if !strings.Contains(rules, need) {
			t.Errorf("no alert for %s", need)
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

// Database connections in the example manifests require TLS (SEC-DATA-4), and nothing in the repository
// parses or transcodes untrusted media: files are stored and served as they came (SEC-CNT-5, CORE-A4).
func TestDatabaseTLSAndNoMediaProcessing(t *testing.T) {
	overlay := repoFile(t, "deploy", "k8s", "overlays", "example", "kustomization.yaml")
	for _, line := range strings.Split(overlay, "\n") {
		if strings.Contains(line, "DATABASE_URL=") && !strings.Contains(line, "sslmode=require") {
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
