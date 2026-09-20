//go:build integration

package server_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/gen/botapi"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/server"
)

// The authorisation matrix (SEC-ISO-1, docs/design/08 section 5): every operation of the
// OpenAPI documents is called by every kind of caller, and the outcome must match what the
// operation's `security` declares. Because it is generated from the documents, adding an
// operation without declaring its callers fails the build, and a new operation is covered the
// moment it exists.

type actor struct {
	name string
	// call performs a request as this actor.
	call func(method, path string, body any) response
}

func denied(r response) bool { return r.Status == 401 || r.Status == 403 }

func TestUserAPIAuthorisationMatrix(t *testing.T) {
	s := newStack(t)
	s.makeUser("root", true)
	s.makeUser("member", false)
	admin, member := s.newClient(), s.newClient()
	admin.login("root")
	member.login("member")
	bot := s.makeBot("m", "example.org")

	spec, err := userapi.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	reqs, err := server.RequirementsFromSpec(spec) // fails for an operation without declared security
	if err != nil {
		t.Fatal(err)
	}

	anonymous := actor{"anonymous", func(m, p string, b any) response { return s.newClient().do(m, p, b) }}
	asMember := actor{"user", member.do}
	asAdmin := actor{"admin", admin.do}
	botKey := actor{"bot key", func(m, p string, b any) response {
		return s.newClient().doWith(m, p, b, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+bot) })
	}}

	checked := 0
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			req := reqs[method+" "+path]
			concrete := fillPath(path, op)
			for _, a := range []actor{anonymous, botKey, asMember, asAdmin} {
				// Destructive operations on the caller's own session would end the test: skip the
				// allowed-caller probes for those; the denied-caller probes are still made.
				allowed := map[string]bool{
					"anonymous": req == server.Anonymous,
					"bot key":   req == server.Anonymous,
					"user":      req == server.Anonymous || req == server.User,
					"admin":     true,
				}[a.name]
				if allowed && slices.ContainsFunc([]string{"logout", "revokeAllSessions", "streamEvents"}, func(id string) bool { return strings.EqualFold(id, op.OperationID) }) {
					continue
				}
				res := a.call(method, concrete, sampleBody(method))
				checked++
				code := res.Code()
				switch {
				case allowed && (code == "unauthenticated" || code == "forbidden" || code == "csrf"):
					t.Errorf("%s %s (%s): %s must be allowed but got %d %s", method, path, op.OperationID, a.name, res.Status, code)
				case !allowed && !denied(res):
					t.Errorf("%s %s (%s): %s must be denied but got %d %s", method, path, op.OperationID, a.name, res.Status, res.Body)
				case !allowed && a.name == "user" && res.Status != 403:
					t.Errorf("%s %s: a signed-in non-admin must get 403, got %d", method, path, res.Status)
				case !allowed && a.name != "user" && res.Status != 401:
					t.Errorf("%s %s: %s must get 401, got %d", method, path, a.name, res.Status)
				}
			}
		}
	}
	if checked < 50 {
		t.Fatalf("matrix ran only %d probes; the spec walk is broken", checked)
	}
	t.Logf("user API: %d probes", checked)
}

func TestBotAPIAuthorisationMatrix(t *testing.T) {
	s := newStack(t)
	s.makeUser("member", false)
	member := s.newClient()
	member.login("member")
	full := s.makeBot("full", "example.org")

	spec, err := botapi.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := server.BotScopesFromSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			scope := scopes[method+" "+path]
			concrete := fillPath(path, op)
			probe := func(bearer string) response { return s.botDo(bearer, method, concrete, sampleBody(method)) }
			if scope == "" {
				if r := probe(""); denied(r) {
					t.Errorf("%s %s is anonymous but got %d", method, path, r.Status)
				}
				continue
			}
			if r := probe(""); r.Status != 401 {
				t.Errorf("%s %s: no key must give 401, got %d", method, path, r.Status)
			}
			if r := probe(member.cookies["__Host-nka"]); r.Status != 401 {
				t.Errorf("%s %s: a user token must give 401, got %d", method, path, r.Status)
			}
			if r := probe(full); denied(r) {
				t.Errorf("%s %s: a full-scope key must be allowed, got %d %s", method, path, r.Status, r.Body)
			}
		}
	}
}

// sampleBody is an empty JSON object for methods that carry a body; handlers answer 400 to it,
// which is fine: the matrix only distinguishes "not allowed" (401/403) from everything else.
func sampleBody(method string) any {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut:
		return map[string]any{}
	}
	return nil
}

// fillPath substitutes path parameters with random identifiers.
func fillPath(path string, op *openapi3.Operation) string {
	out := path
	for {
		i := strings.Index(out, "{")
		if i < 0 {
			return out
		}
		j := strings.Index(out, "}")
		out = out[:i] + uuid.NewString() + out[j+1:]
	}
}

var _ = fmt.Sprintf

// The "other user" actor of the matrix (SEC-ISO-2, SEC-ISO-3): every operation that names an
// object is called by a signed-in user with the REAL identifiers of another user's objects, and
// the answer must be identical to the answer for identifiers that do not exist at all, so
// nobody can tell them apart. Bodies are valid, so the lookup is what decides the outcome.
func TestForeignObjectsAnswerLikeMissingOnes(t *testing.T) {
	s := newStack(t)
	victim, attacker := s.appUser("victim"), s.appUser("attacker")
	page := victim.page("P")
	cat := victim.category(page, "C")
	note := victim.note(cat, "secret", nil)
	var sessions struct{ Items []struct{ ID string } }
	victim.get("/api/v1/me/sessions", &sessions)
	key := s.makeBot("m", "example.org")
	insts, _ := s.svc.Bots.ListInstances(t.Context())
	vid := s.lookupUser("victim")
	pc, _ := s.svc.Bots.CreatePairingCode(t.Context(), vid, "", &insts[0].ID)
	s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code, "sender": "@victim:example.org", "conversation": "!r"})
	var idents struct{ Items []struct{ ID string } }
	victim.get("/api/v1/me/identities", &idents)

	// Which kind of object each path parameter names, by the collection it follows.
	real := map[string]string{"notes": note.ID, "pages": page, "categories": cat, "sessions": sessions.Items[0].ID, "identities": idents.Items[0].ID}
	bodies := map[string]any{
		"updatePage": map[string]any{"name": "x"}, "updateCategory": map[string]any{"name": "x"},
		"moveNote": map[string]any{"category_id": nil}, "addNotePart": map[string]any{"type": "text", "text": "x"},
		"editNotePart": map[string]any{"text": "x"},
	}

	spec, _ := userapiSpec()
	probes := 0
	for path, item := range spec.Paths.Map() {
		if strings.Contains(path, "/admin/") || !strings.Contains(path, "{") {
			continue
		}
		for method, op := range item.Operations() {
			if strings.EqualFold(op.OperationID, "streamEvents") {
				continue
			}
			realPath, missingPath := path, path
			segments := strings.Split(path, "/")
			for i, seg := range segments {
				if !strings.HasPrefix(seg, "{") {
					continue
				}
				kind := segments[i-1]
				id, ok := real[kind]
				if seg == "{partId}" {
					id, ok = note.Parts[0].ID, true
				}
				if !ok {
					t.Fatalf("%s %s: no fixture for %q; extend the matrix", method, path, kind)
				}
				realPath = strings.Replace(realPath, seg, id, 1)
				missingPath = strings.Replace(missingPath, seg, uuid.NewString(), 1)
			}
			body := bodies[strings.ToLower(op.OperationID[:1])+op.OperationID[1:]]
			if body == nil && (method == "POST" || method == "PATCH") {
				body = map[string]any{}
			}
			foreign := attacker.c.do(method, realPath, body)
			missing := attacker.c.do(method, missingPath, body)
			probes++
			if foreign.Status != missing.Status || foreign.Code() != missing.Code() {
				t.Errorf("%s %s: a foreign id answers %d %q but a missing one %d %q", method, path,
					foreign.Status, foreign.Code(), missing.Status, missing.Code())
			}
			if foreign.Status != 404 {
				t.Errorf("%s %s: foreign id answered %d, want 404 (%s)", method, path, foreign.Status, foreign.Body)
			}
		}
	}
	if probes < 15 {
		t.Fatalf("only %d probes ran", probes)
	}
	// The victim's data is untouched.
	if got := texts(victim.column(page, 0)); got != "secret" {
		t.Fatalf("victim's column: %s", got)
	}
}
