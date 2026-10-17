// Package api is the med-api REST service (/api/v1): session authentication, role-based access control,
// optimistic locking of configuration documents (ETag / If-Match), audit of every change, reports with
// CSV export, the Template Lab and a server-sent event stream of the live log.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"dxmed/internal/auth"
	"dxmed/internal/engine"
	"dxmed/internal/ops"
	"dxmed/internal/store"
)

// MaxBody bounds request bodies (Template Lab samples are the largest).
const MaxBody = 16 << 20

// Server serves the API.
type Server struct {
	st  *store.Store
	eng *engine.Engine
	log *slog.Logger

	Now        func() time.Time
	SessionTTL time.Duration
	SSEPoll    time.Duration
	// TrustProxy takes the client address from X-Forwarded-For (set when the UI proxy is the only client).
	TrustProxy bool

	lim *limiter
}

// New returns a server. eng is used to close partials on request.
func New(st *store.Store, eng *engine.Engine, log *slog.Logger) *Server {
	return &Server{st: st, eng: eng, log: log, Now: time.Now, SessionTTL: 8 * time.Hour, SSEPoll: time.Second,
		lim: &limiter{max: 5, window: 5 * time.Minute, fails: map[string][]time.Time{}}}
}

// principal is the authenticated caller.
type principal struct {
	User  string
	Roles []string
	IP    string
	token []byte
}

type handler func(w http.ResponseWriter, r *http.Request, p *principal) error

// Error is an API error with an HTTP status.
type Error struct {
	Status  int    `json:"-"`
	Message string `json:"error"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Message }

func errorf(status int, format string, a ...any) *Error {
	return &Error{Status: status, Message: fmt.Sprintf(format, a...)}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return s.recoverer(headers(http.MaxBytesHandler(mux, MaxBody)))
}

func (s *Server) handle(mux *http.ServeMux, pattern, role string, h handler) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		p := &principal{IP: s.clientIP(r)}
		if role != "" {
			var err *Error
			if p, err = s.authenticate(r, p.IP); err != nil {
				writeErr(w, err)
				return
			}
			if !auth.Allows(p.Roles, role) {
				writeErr(w, errorf(http.StatusForbidden, "role %s required", role))
				return
			}
		}
		if err := h(w, r, p); err != nil {
			s.fail(w, r, err)
		}
	})
}

func (s *Server) authenticate(r *http.Request, ip string) (*principal, *Error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return nil, errorf(http.StatusUnauthorized, "authentication required")
	}
	h := auth.TokenHash(tok)
	sess, err := s.st.GetSession(r.Context(), h)
	if err != nil {
		return nil, errorf(http.StatusUnauthorized, "session expired")
	}
	return &principal{User: sess.Username, Roles: sess.Roles, IP: ip, token: h}, nil
}

func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxy {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			ip, _, _ := strings.Cut(f, ",")
			return strings.TrimSpace(ip)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *Error
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &ae):
	case errors.As(err, &mbe):
		ae = errorf(http.StatusRequestEntityTooLarge, "request body over %d bytes", mbe.Limit)
	case errors.Is(err, store.ErrNotFound):
		ae = errorf(http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		ae = errorf(http.StatusConflict, "%s", err)
	case errors.Is(err, ops.ErrInvalid), errors.Is(err, ops.ErrGate):
		ae = errorf(http.StatusUnprocessableEntity, "%s", err)
	case errors.Is(err, context.Canceled):
		return
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		ae = errorf(http.StatusInternalServerError, "internal error")
	}
	writeErr(w, ae)
}

func writeErr(w http.ResponseWriter, e *Error) { writeJSON(w, e.Status, e) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, v any) error {
	writeJSON(w, http.StatusOK, v)
	return nil
}

func decodeBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return errorf(http.StatusBadRequest, "invalid request body: %s", err)
	}
	return nil
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "path", r.URL.Path, "panic", fmt.Sprint(v))
				writeErr(w, errorf(http.StatusInternalServerError, "internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// limiter blocks a key after max failures within window (login brute force protection).
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func (l *limiter) recent(key string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if keep == nil {
		delete(l.fails, key)
	} else {
		l.fails[key] = keep
	}
	return keep
}

func (l *limiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key, now)) >= l.max
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.recent(key, now), now)
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
