package server

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/Niboor/notekeeper/core/internal/gen/botapi"
	"github.com/Niboor/notekeeper/core/internal/gen/publicapi"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
)

// The three APIs follow one set of conventions (NFR-API2, NFR-API3, BOT-1): a major version in every path,
// UUID identifiers, RFC 3339 times, problem-details errors, and separate documents for user, bot and share.
func TestAPIsFollowTheConventions(t *testing.T) {
	for name, load := range map[string]struct {
		spec   func() (*openapi3.T, error)
		prefix string
	}{
		"user":   {userapi.GetSpec, "/api/v1/"},
		"bot":    {botapi.GetSpec, "/bot/v1/"},
		"public": {publicapi.GetSpec, "/api/public/v1/"},
	} {
		spec, err := load.spec()
		if err != nil {
			t.Fatal(err)
		}
		for path, item := range spec.Paths.Map() {
			if !strings.HasPrefix(path, load.prefix) {
				t.Errorf("%s: %s is not under %s", name, path, load.prefix)
			}
			for method, op := range item.Operations() {
				if strings.HasSuffix(path, "/version") || strings.HasSuffix(path, "/events") {
					continue // a build banner and the event stream have no error document of their own
				}
				if op.Responses.Default() == nil {
					t.Errorf("%s %s %s has no default (problem) response", name, method, path)
				} else if ref := op.Responses.Default().Ref; !strings.Contains(ref, "Problem") {
					t.Errorf("%s %s %s: the default response is %q, not a problem document", name, method, path, ref)
				}
				for _, p := range append(item.Parameters, op.Parameters...) {
					v := p.Value
					if strings.Contains(path, "/conversations/") {
						continue // a platform's own conversation id
					}
					if v.In == "path" && (v.Name == "id" || strings.HasSuffix(v.Name, "Id")) && v.Schema.Value.Format != "uuid" {
						t.Errorf("%s %s: path parameter %s is not a UUID", name, path, v.Name)
					}
				}
			}
		}
		var check func(where string, s *openapi3.SchemaRef, depth int)
		check = func(where string, s *openapi3.SchemaRef, depth int) {
			if s == nil || s.Value == nil || depth > 6 {
				return
			}
			for prop, ps := range s.Value.Properties {
				if strings.HasSuffix(prop, "_at") && ps.Value != nil && ps.Value.Type != nil && ps.Value.Type.Is("string") && ps.Value.Format != "date-time" {
					t.Errorf("%s: %s.%s is a time without the date-time (RFC 3339) format", name, where, prop)
				}
				if (prop == "id" || strings.HasSuffix(prop, "_id")) && ps.Value != nil && ps.Value.Type != nil && ps.Value.Type.Is("string") && ps.Value.Format != "uuid" &&
					!strings.Contains(prop, "external") && !strings.Contains(prop, "message") && !strings.Contains(prop, "conversation") && !strings.Contains(prop, "event") && !strings.Contains(prop, "client") && prop != "request_id" {
					t.Errorf("%s: %s.%s is an identifier without the uuid format", name, where, prop)
				}
				check(where+"."+prop, ps, depth+1)
			}
			if s.Value.Items != nil {
				check(where+"[]", s.Value.Items, depth+1)
			}
		}
		for sn, s := range spec.Components.Schemas {
			check(sn, s, 0)
		}
	}
}
