package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Requirement says who may call an operation, as declared by its `security` in the OpenAPI
// document. The document is the single source of truth: the router enforces it, and the
// authorisation matrix test is generated from it (SEC-ISO-1).
type Requirement int

// Requirements.
const (
	Anonymous Requirement = iota
	User
	Admin
)

func (r Requirement) String() string { return [...]string{"anonymous", "user", "admin"}[r] }

// opKey identifies an operation by method and chi route pattern.
func opKey(method, pattern string) string { return strings.ToUpper(method) + " " + pattern }

// RequirementsFromSpec reads the security declarations of every operation. An operation without
// a `security` key is an error: nothing may be reachable by accident.
func RequirementsFromSpec(doc *openapi3.T) (map[string]Requirement, error) {
	out := map[string]Requirement{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.Security == nil {
				return nil, fmt.Errorf("%s %s (%s) declares no security: use `security: []` for anonymous", method, path, op.OperationID)
			}
			req := Anonymous
			for _, sr := range *op.Security {
				for _, scopes := range sr {
					if req < User {
						req = User
					}
					for _, sc := range scopes {
						if sc == "admin" {
							req = Admin
						}
					}
				}
			}
			out[opKey(method, path)] = req
		}
	}
	return out, nil
}

// unsafeMethod reports whether a method changes state (CSRF protections apply, SEC-AUTH-8).
func unsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// BotScopesFromSpec reads which scope each bot API operation needs ("" = anonymous).
func BotScopesFromSpec(doc *openapi3.T) (map[string]string, error) {
	out := map[string]string{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.Security == nil {
				return nil, fmt.Errorf("%s %s (%s) declares no security", method, path, op.OperationID)
			}
			scope := ""
			for _, sr := range *op.Security {
				for _, scopes := range sr {
					if len(scopes) > 0 {
						scope = scopes[0]
					} else {
						scope = "any"
					}
				}
			}
			out[opKey(method, path)] = scope
		}
	}
	return out, nil
}
