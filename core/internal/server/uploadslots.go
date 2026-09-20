package server

import (
	"net/http"
	"strings"

	"github.com/Niboor/notekeeper/core/internal/httpx"
)

// uploadSlots caps the uploads one caller runs at once, so a few slow large uploads cannot hold every
// connection (SEC-API-4). It applies to the two streaming upload routes and to nothing else.
func uploadSlots(l *limits) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !uploadRoute(r) {
				next.ServeHTTP(w, r)
				return
			}
			key := "ip:" + httpx.ClientIP(r, l.trusted)
			if p, ok := PrincipalFrom(r.Context()); ok {
				key = "user:" + p.UserID.String()
			} else if strings.HasPrefix(r.URL.Path, "/bot/") {
				key = "bot:" + strings.TrimSpace(r.Header.Get("X-External-User"))
			}
			release, ok := l.startUpload(key)
			if !ok {
				httpx.WriteError(w, r, &httpx.Error{Status: http.StatusTooManyRequests, Code: "too_many_uploads", RetryAfter: 5})
				return
			}
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}
