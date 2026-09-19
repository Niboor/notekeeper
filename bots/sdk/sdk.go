// Package sdk is the Go client for the Notekeeper bot API, shared by every bot (docs/design/02
// section 2). The generated client lives in botclient; this package adds authentication.
package sdk

import (
	"context"
	"net/http"

	"notekeeper/bots/sdk/botclient"
)

// New returns a bot API client that authenticates with the bot key (AUTH-B1) and tags every
// request with requestID so one chat message can be followed end to end (NFR-O1).
func New(baseURL, botKey string, httpClient *http.Client) (*botclient.ClientWithResponses, error) {
	return botclient.NewClientWithResponses(baseURL,
		botclient.WithHTTPClient(httpClient),
		botclient.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+botKey)
			if id, ok := ctx.Value(requestIDKey{}).(string); ok && id != "" {
				req.Header.Set("X-Request-ID", id)
			}
			return nil
		}))
}

type requestIDKey struct{}

// WithRequestID returns ctx carrying a request id that New's client forwards to Core.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}
