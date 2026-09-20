package server

import (
	"net/http"
	"net/netip"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/ratelimit"
)

// limits keeps one caller from degrading the service for the others (SEC-API-4, SEC-BOT-10, NFR-S4).
// The buckets are per replica, so the effective ceiling is the number of replicas times the rate; the
// limits that must hold across replicas (login and pairing attempts, link creation, exports) are kept
// in PostgreSQL. Refusals are counted, so a burst of them can be alerted on (SEC-AUD-3).
type limits struct {
	perIP, perUser, perBot, perIdentity *ratelimit.Limiter
	maxUploads                          int
	trusted                             []netip.Prefix
	refused                             *prometheus.CounterVec

	mu      sync.Mutex
	uploads map[string]int
}

func newLimits(cfg config.Config, trusted []netip.Prefix, reg prometheus.Registerer) *limits {
	// A configuration that leaves a limit unset (tests, embedded use) gets the documented default.
	def := func(v, d int) int {
		if v <= 0 {
			return d
		}
		return v
	}
	cfg.RateIPPerMin, cfg.RateUserPerMin = def(cfg.RateIPPerMin, 1200), def(cfg.RateUserPerMin, 900)
	cfg.RateBotPerMin, cfg.RateIdentityPerMin, cfg.MaxConcurrentUpload = def(cfg.RateBotPerMin, 6000), def(cfg.RateIdentityPerMin, 600), def(cfg.MaxConcurrentUpload, 4)
	burst := func(perMin int) float64 { return float64(max(20, perMin/3)) }
	l := &limits{
		perIP:       ratelimit.New(float64(cfg.RateIPPerMin), burst(cfg.RateIPPerMin)),
		perUser:     ratelimit.New(float64(cfg.RateUserPerMin), burst(cfg.RateUserPerMin)),
		perBot:      ratelimit.New(float64(cfg.RateBotPerMin), burst(cfg.RateBotPerMin)),
		perIdentity: ratelimit.New(float64(cfg.RateIdentityPerMin), burst(cfg.RateIdentityPerMin)),
		maxUploads:  cfg.MaxConcurrentUpload, trusted: trusted, uploads: map[string]int{},
		refused: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nk_rate_limited_total", Help: "Requests refused by a rate limit, by scope."}, []string{"scope"}),
	}
	if reg != nil {
		reg.MustRegister(l.refused)
	}
	return l
}

func (l *limits) refuse(w http.ResponseWriter, r *http.Request, scope string, lim *ratelimit.Limiter, key string) {
	l.refused.WithLabelValues(scope).Inc()
	wait := int(lim.RetryAfter(key, 1).Seconds()) + 1
	httpx.WriteError(w, r, &httpx.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: wait})
}

// ip limits every request by client address, signed in or not.
func (l *limits) ip(next http.Handler) http.Handler {
	if l == nil || l.perIP == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIP(r, l.trusted)
		if !l.perIP.Allow(ip) {
			l.refuse(w, r, "ip", l.perIP, ip)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// user limits the requests of a signed-in person, after authentication.
func (l *limits) user(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFrom(r.Context()); ok {
			if key := p.UserID.String(); !l.perUser.Allow(key) {
				l.refuse(w, r, "user", l.perUser, key)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// bot limits a bot instance's requests as a whole, after authentication.
func (l *limits) bot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := r.Context().Value(botPrincipalKey{}).(*bots.Principal); ok && b != nil {
			if key := b.InstanceID.String(); !l.perBot.Allow(key) {
				l.refuse(w, r, "bot", l.perBot, key)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// identityAllowed limits what one linked person can push through a bot, on top of the instance limit.
func (l *limits) identityAllowed(key string) (ok bool, retry int) {
	if l.perIdentity.Allow(key) {
		return true, 0
	}
	l.refused.WithLabelValues("identity").Inc()
	return false, int(l.perIdentity.RetryAfter(key, 1).Seconds()) + 1
}

// startUpload reserves one of the caller's upload slots; release must be called when it ends.
func (l *limits) startUpload(key string) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.uploads[key] >= l.maxUploads {
		l.refused.WithLabelValues("uploads").Inc()
		return nil, false
	}
	l.uploads[key]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.uploads[key]--; l.uploads[key] <= 0 {
			delete(l.uploads, key)
		}
	}, true
}
