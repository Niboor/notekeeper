package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// Every environment variable the code reads is documented, and the documentation names none that no code
// reads: configuration is externalised and documented (NFR-O5, SEC-BASE-5).
func TestConfigurationDocumentsEveryVariable(t *testing.T) {
	root := repoRoot(t)
	doc, err := os.ReadFile(filepath.Join(root, "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile("`((?:NK|MX)_[A-Z0-9_]+|APP_HOST|SHARE_HOST|CORE_USER_UPSTREAM|CORE_PUBLIC_UPSTREAM)`").FindAllStringSubmatch(string(doc), -1) {
		documented[m[1]] = true
	}
	read := map[string]bool{}
	quoted := regexp.MustCompile(`"((?:NK|MX)_[A-Z0-9_]+)"`)
	for _, base := range []string{"core", "bots"} {
		_ = filepath.WalkDir(filepath.Join(root, base), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/gen/") || strings.Contains(path, "botclient") {
				return nil
			}
			b, _ := os.ReadFile(path)
			for _, m := range quoted.FindAllStringSubmatch(string(b), -1) {
				read[m[1]] = true
			}
			return nil
		})
	}
	templates, _ := filepath.Glob(filepath.Join(root, "deploy", "nginx", "*.template"))
	for _, tpl := range templates {
		b, _ := os.ReadFile(tpl)
		for _, m := range regexp.MustCompile(`\$\{([A-Z_]+)\}`).FindAllStringSubmatch(string(b), -1) {
			read[m[1]] = true
		}
	}
	tooling := map[string]bool{"NK_TEST_LOG": true, "NK_TEST_VERBOSE": true, "NK_TEST_PG_IMAGE": true, "NK_TEST_PG_IMAGES": true, "NK_TEST_SYNAPSE_IMAGE": true, "NK_PERF_NOTES": true}
	var missing, unknown []string
	for v := range read {
		if !documented[v] {
			missing = append(missing, v)
		}
	}
	for v := range documented {
		if !read[v] && !tooling[v] {
			unknown = append(unknown, v)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	if len(read) < 40 {
		t.Fatalf("found only %d variables in the code; the scan is broken", len(read))
	}
	if len(missing) > 0 || len(unknown) > 0 {
		t.Fatalf("docs/configuration.md is out of step with the code.\nRead but not documented: %v\nDocumented but not read: %v", missing, unknown)
	}
}

// The documentation says plainly that notes are stored readable by the server, where a new user reads it
// first, and the README points to it (NFR-S5).
func TestPrivacyNoteIsStatedClearly(t *testing.T) {
	root := repoRoot(t)
	guide, err := os.ReadFile(filepath.Join(root, "docs", "user-guide.md"))
	if err != nil {
		t.Fatal(err)
	}
	head := string(guide)
	if i := strings.Index(head, "## Getting started"); i > 0 {
		head = head[:i] // it must come before anything else a person is told to do
	}
	for _, need := range []string{"end-to-end encrypted", "are not", "can read your notes", "administrator", "share link"} {
		if !strings.Contains(head, need) {
			t.Errorf("the privacy note before \"Getting started\" does not say %q", need)
		}
	}
	readme, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if !strings.Contains(string(readme), "who can read your notes") {
		t.Error("the README does not point to the privacy note")
	}
}

// The operator can find how to install, back up and restore (attachments included), rotate secrets and
// watch the system, and a developer can find how to add a chat platform (NFR-Q4, NFR-R4, SEC-BASE-4, SEC-DATA-7).
func TestOperationsAndDeveloperDocumentationExist(t *testing.T) {
	root := repoRoot(t)
	need := map[string][]string{
		"docs/operations.md": {"Backup and restore", "pg_dump", "attachments", "Restore", "Rotating secrets", "Token keys", "Encrypt and restrict backups", "retention", "Upgrading", "Watching it"},
		"docs/bot-guide.md":  {"outbox", "event_id", "at least once", "lifecycle", "Attachments", "Testing"},
		"docs/user-guide.md": {"Reminders", "Sharing", "Export my data", "Delete my account"},
	}
	for file, phrases := range need {
		b, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range phrases {
			if !strings.Contains(string(b), p) {
				t.Errorf("%s does not cover %q", file, p)
			}
		}
	}
	design, err := os.ReadFile(filepath.Join(root, "docs", "design", "README.md"))
	if err != nil || len(design) < 500 {
		t.Fatal("the architecture documentation is missing")
	}
}
