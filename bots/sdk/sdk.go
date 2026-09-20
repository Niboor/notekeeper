// Package sdk is the Go client for the Notekeeper bot API, shared by every bot (docs/design/02
// section 2). The generated client lives in botclient; this package adds authentication, request
// ids and the retry policy every bot needs: transient failures are retried with backoff until the
// caller's context ends, so a Core outage stalls a bot instead of losing messages (NFR-R1).
package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/bots/sdk/botclient"
)

// New returns a bot API client that authenticates with the bot key (AUTH-B1) and tags every
// request with a request id from ctx so one chat message can be followed end to end (NFR-O1).
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

// PermanentError means Core refused a request in a way that retrying cannot fix (a malformed
// event, a revoked key). The bot must record it and move on rather than block forever.
type PermanentError struct {
	Status int
	Code   string
}

func (e *PermanentError) Error() string {
	return fmt.Sprintf("core refused the request: %d %s", e.Status, e.Code)
}

// Client wraps the generated client with the retry policy.
type Client struct {
	API *botclient.ClientWithResponses
	// Backoff returns the wait before retry number attempt (1-based). Defaults to 1s doubling to 30s.
	Backoff func(attempt int) time.Duration
	// OnRetry, if set, is called for every retried failure (logging, metrics).
	OnRetry func(op string, attempt int, err error)
}

// NewClient creates a Client for a Core at baseURL.
func NewClient(baseURL, botKey string, httpClient *http.Client) (*Client, error) {
	api, err := New(baseURL, botKey, httpClient)
	if err != nil {
		return nil, err
	}
	return &Client{API: api}, nil
}

func (c *Client) backoff(attempt int) time.Duration {
	if c.Backoff != nil {
		return c.Backoff(attempt)
	}
	d := time.Second << min(attempt-1, 5)
	return min(d, 30*time.Second)
}

// do runs call until it succeeds, fails permanently or ctx ends. call returns the HTTP status
// (0 when the request itself failed), the problem code, and an error for transport failures.
func (c *Client) do(ctx context.Context, op string, call func() (status int, code string, err error)) error {
	for attempt := 1; ; attempt++ {
		status, code, err := call()
		switch {
		case err == nil && status >= 200 && status < 300:
			return nil
		case err == nil && status >= 400 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout:
			return &PermanentError{Status: status, Code: code}
		}
		if err == nil {
			err = fmt.Errorf("core answered %d", status)
		}
		if c.OnRetry != nil {
			c.OnRetry(op, attempt, err)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(c.backoff(attempt)):
		}
	}
}

func problemCode(p *botclient.Problem) string {
	if p != nil && p.Code != nil {
		return *p.Code
	}
	return ""
}

// PostEvent sends an event, retrying until Core has answered it.
func (c *Client) PostEvent(ctx context.Context, ev botclient.Event) (*botclient.EventResult, error) {
	var out *botclient.EventResult
	err := c.do(ctx, "post event", func() (int, string, error) {
		res, err := c.API.PostEventWithResponse(ctx, ev)
		if err != nil {
			return 0, "", err
		}
		out = res.JSON200
		return res.StatusCode(), problemCode(res.ApplicationproblemJSONDefault), nil
	})
	return out, err
}

// PostCommand sends a command, retrying until Core has answered it.
func (c *Client) PostCommand(ctx context.Context, cmd botclient.Command) (*botclient.CommandResult, error) {
	var out *botclient.CommandResult
	err := c.do(ctx, "post command", func() (int, string, error) {
		res, err := c.API.PostCommandWithResponse(ctx, cmd)
		if err != nil {
			return 0, "", err
		}
		out = res.JSON200
		return res.StatusCode(), problemCode(res.ApplicationproblemJSONDefault), nil
	})
	return out, err
}

// Heartbeat tells Core the bot is alive (WEB-12). It is not retried: the next tick will do.
func (c *Client) Heartbeat(ctx context.Context) error {
	res, err := c.API.PostHeartbeatWithResponse(ctx)
	if err != nil {
		return err
	}
	if res.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("heartbeat: core answered %d", res.StatusCode())
	}
	return nil
}

// Reachable reports whether Core answers its version endpoint (for readiness).
func (c *Client) Reachable(ctx context.Context) bool {
	res, err := c.API.GetBotVersionWithResponse(ctx)
	return err == nil && res.StatusCode() == http.StatusOK
}

// UploadAttempts is how often Upload tries a file before giving up on it. A file is different
// from a message: a chat must not stall for one large photo, so the bot records a failed
// attachment instead of retrying forever (CORE-A9).
const UploadAttempts = 3

// Body is one attempt at reading a file.
type Body struct {
	Reader io.Reader
	// Size is the exact number of bytes Reader will deliver.
	Size int64
	// Finish is called after the request. It reports whether the source turned out to be intact, for
	// example an encrypted stream whose checksum only verifies at the end. Its error fails the upload
	// even when Core accepted the bytes; the orphaned upload is removed by Core's janitor.
	Finish func() error
}

// Source opens a file for one attempt. An error that is a *PermanentError is not retried.
type Source func() (Body, error)

// Upload stores a file for a linked chat user (BOT-6). The source is opened again for every
// attempt, so a retry starts from the beginning.
func (c *Client) Upload(ctx context.Context, id uuid.UUID, externalUser, filename, mediaType string, open Source) error {
	params := &botclient.PutUploadParams{XExternalUser: externalUser, XFilename: url.PathEscape(filename)}
	if mediaType != "" {
		params.XMediaType = &mediaType
	}
	var lastErr error
	for attempt := 1; attempt <= UploadAttempts; attempt++ {
		body, err := open()
		var perm *PermanentError
		if errors.As(err, &perm) {
			return err
		}
		if err == nil {
			var status int
			var code string
			status, code, err = c.putUpload(ctx, id, params, body.Size, body.Reader)
			if ferr := body.Finish(); ferr != nil && err == nil && status >= 200 && status < 300 {
				return fmt.Errorf("source failed verification: %w", ferr)
			}
			switch {
			case err == nil && status >= 200 && status < 300:
				return nil
			case err == nil && status >= 400 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout && status != http.StatusConflict:
				return &PermanentError{Status: status, Code: code}
			case err == nil:
				// 409: an earlier attempt at this upload id (a bot that was killed mid-upload) is
				// still being cleaned up by Core. That passes, so it is worth another attempt.
				err = fmt.Errorf("core answered %d", status)
			}
		}
		lastErr = err
		if c.OnRetry != nil {
			c.OnRetry("upload", attempt, err)
		}
		if attempt < UploadAttempts {
			select {
			case <-ctx.Done():
				return errors.Join(ctx.Err(), err)
			case <-time.After(c.backoff(attempt)):
			}
		}
	}
	return lastErr
}

func (c *Client) putUpload(ctx context.Context, id uuid.UUID, params *botclient.PutUploadParams, size int64, body io.Reader) (int, string, error) {
	res, err := c.API.PutUploadWithBodyWithResponse(ctx, id, params, "application/octet-stream", body,
		func(_ context.Context, req *http.Request) error { req.ContentLength = size; return nil })
	if err != nil {
		return 0, "", err
	}
	return res.StatusCode(), problemCode(res.ApplicationproblemJSONDefault), nil
}

// ClaimOutbox asks Core for messages to send to chats, waiting up to wait for one to appear
// (BOT-12). It makes one attempt; the caller's loop owns the pacing.
func (c *Client) ClaimOutbox(ctx context.Context, wait time.Duration, limit int) ([]botclient.OutboxItem, error) {
	secs := int(wait / time.Second)
	res, err := c.API.ClaimOutboxWithResponse(ctx, &botclient.ClaimOutboxParams{Wait: &secs, Limit: &limit})
	if err != nil {
		return nil, err
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf("claim outbox: core answered %d", res.StatusCode())
	}
	return res.JSON200.Items, nil
}

// ReportOutbox tells Core what happened to a claimed item, retrying until Core has heard it. A
// 404 means the lease was lost meanwhile and the item belongs to someone else now: not an error.
func (c *Client) ReportOutbox(ctx context.Context, id uuid.UUID, result botclient.OutboxResult) error {
	err := c.do(ctx, "report outbox result", func() (int, string, error) {
		res, err := c.API.PostOutboxResultWithResponse(ctx, id, result)
		if err != nil {
			return 0, "", err
		}
		return res.StatusCode(), problemCode(res.ApplicationproblemJSONDefault), nil
	})
	var perm *PermanentError
	if errors.As(err, &perm) && perm.Status == http.StatusNotFound {
		return nil
	}
	return err
}
