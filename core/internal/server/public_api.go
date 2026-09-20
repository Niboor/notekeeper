package server

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/gen/publicapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/ratelimit"
	"github.com/Niboor/notekeeper/core/internal/shares"
	"github.com/Niboor/notekeeper/core/internal/version"
)

// publicAPI serves the share page's data. It accepts no credentials but the share token, sets no
// cookies, and reaches only the shared note and its attachments (CORE-SH7, SEC-SHR-4, SEC-SHR-5).
type publicAPI struct {
	shares  *shares.Service
	trusted []netip.Prefix

	perIP   *ratelimit.Limiter // requests per address
	perLink *ratelimit.Limiter // requests per link
	bytes   *ratelimit.Limiter // download volume per link, in MiB (CORE-SH9, SEC-SHR-8)
}

func newPublicAPI(s *shares.Service, trusted []netip.Prefix) *publicAPI {
	return &publicAPI{shares: s, trusted: trusted,
		perIP: ratelimit.New(120, 40), perLink: ratelimit.New(300, 100), bytes: ratelimit.New(200.0/60, 100)}
}

type tokenKey struct{}

const shareTokenHeader = "X-Share-Token"

// guard runs before the generated code: it limits by address, extracts the token, limits by link, and
// makes every response non-cacheable and non-indexable with no cookie in either direction (SEC-SHR-5,
// SEC-SHR-6). The token comes from a header only; a request without one is answered exactly like an
// unknown link (SEC-SHR-3).
func (p *publicAPI) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		r.Header.Del("Cookie") // nothing on this listener may depend on a cookie
		if r.URL.Path == "/api/public/v1/version" {
			next.ServeHTTP(w, r)
			return
		}
		ip := httpx.ClientIP(r, p.trusted)
		if !p.perIP.Allow(ip) {
			tooMany(w, r, p.perIP.RetryAfter(ip, 1).Seconds())
			return
		}
		token := r.Header.Get(shareTokenHeader)
		if token == "" {
			httpx.WriteError(w, r, errNotFound)
			return
		}
		key := shares.LinkKey(token)
		if !p.perLink.Allow(key) {
			tooMany(w, r, p.perLink.RetryAfter(key, 1).Seconds())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, token)))
	})
}

func tooMany(w http.ResponseWriter, r *http.Request, seconds float64) {
	httpx.WriteError(w, r, &httpx.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: max(1, int(seconds+0.999))})
}

func tokenOf(ctx context.Context) string {
	t, _ := ctx.Value(tokenKey{}).(string)
	return t
}

func (p *publicAPI) GetPublicVersion(context.Context, publicapi.GetPublicVersionRequestObject) (publicapi.GetPublicVersionResponseObject, error) {
	return publicapi.GetPublicVersion200JSONResponse{Version: version.Version}, nil
}

func (p *publicAPI) GetSharedNote(ctx context.Context, _ publicapi.GetSharedNoteRequestObject) (publicapi.GetSharedNoteResponseObject, error) {
	n, err := p.shares.Open(ctx, tokenOf(ctx))
	if err != nil {
		return nil, err
	}
	out := publicapi.GetSharedNote200JSONResponse{CreatedAt: n.CreatedAt, ExpiresAt: n.ExpiresAt, Parts: make([]publicapi.SharedPart, len(n.Parts))}
	for i, sp := range n.Parts {
		part := publicapi.SharedPart{Kind: publicapi.SharedPartKind(sp.Kind)}
		if sp.Text != "" {
			t := sp.Text
			part.Text = &t
		}
		if sp.FailedFilename != "" {
			f := sp.FailedFilename
			part.FailedFilename = &f
		}
		if a := sp.Attachment; a != nil {
			part.Attachment = &struct {
				Filename  string    `json:"filename"`
				Id        uuid.UUID `json:"id"`
				MediaType string    `json:"media_type"`
				Size      int64     `json:"size"`
			}{Filename: a.Filename, Id: a.ID, MediaType: a.MediaType, Size: a.Size}
		}
		out.Parts[i] = part
	}
	return out, nil
}

// sharedDownload is a file served to the holder of a link: never cached, and the same content-type
// rules as in the app (SEC-CNT-3, CORE-SH8).
type sharedDownload struct{ attachmentDownload }

func (d sharedDownload) VisitGetSharedAttachmentResponse(w http.ResponseWriter) error {
	d.cache = "no-store"
	return d.serve(w)
}

func (p *publicAPI) GetSharedAttachment(ctx context.Context, req publicapi.GetSharedAttachmentRequestObject) (publicapi.GetSharedAttachmentResponseObject, error) {
	token := tokenOf(ctx)
	reader, att, err := p.shares.OpenAttachment(ctx, token, req.Id)
	if err != nil {
		return nil, err
	}
	// A ceiling on what one link may move, so a viral link cannot exhaust the server (CORE-SH9).
	mib := float64(att.Size) / (1 << 20)
	if key := shares.LinkKey(token); !p.bytes.AllowN(key, mib) {
		return nil, &httpx.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: max(1, int(p.bytes.RetryAfter(key, mib).Seconds()))}
	}
	_, r := httpx.HTTPFrom(ctx)
	return sharedDownload{attachmentDownload{r: r, reader: reader, att: att}}, nil
}

var _ = strconv.Itoa
