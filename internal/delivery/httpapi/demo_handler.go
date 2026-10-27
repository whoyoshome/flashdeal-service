package httpapi

import (
	"context"
	"embed"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

//go:embed demo/index.html
var demoFS embed.FS

// DemoStarter opens an isolated flash sale for the public demo page.
type DemoStarter interface {
	Execute(ctx context.Context) (usecase.DemoSession, error)
}

// TokenIssuer mints HS256 JWTs for demo buyers. platform/auth.Service
// implements it.
type TokenIssuer interface {
	Issue(subject string, role auth.Role, ttl time.Duration) (string, error)
}

type demoSessionResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	DealID      int64  `json:"deal_id"`
	Stock       int    `json:"stock"`
	UserID      string `json:"user_id"`
}

func (h *handlers) demoPage(w http.ResponseWriter, r *http.Request) {
	body, err := fs.ReadFile(demoFS, "demo/index.html")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *handlers) demoSession(w http.ResponseWriter, r *http.Request) {
	if h.demo == nil || h.issuer == nil {
		h.routeNotFound(w, r)
		return
	}
	if h.demoLimiter != nil {
		res, err := h.demoLimiter.Allow(r.Context(), "demo:"+clientIP(r))
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		if !res.Allowed {
			w.Header().Set("Retry-After", "1")
			h.writeJSON(w, r, http.StatusTooManyRequests, errorBody{
				Code: "RATE_LIMITED", Message: "demo session rate limit",
				RequestID: middleware.RequestIDFrom(r.Context()),
			})
			return
		}
	}
	session, err := h.demo.Execute(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	tok, err := h.issuer.Issue(session.UserID, auth.RoleUser, usecase.DemoSessionTTL)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusCreated, demoSessionResponse{
		AccessToken: tok,
		TokenType:   "Bearer",
		ExpiresIn:   int(usecase.DemoSessionTTL.Seconds()),
		DealID:      session.Deal.ID(),
		Stock:       session.Deal.TotalStock(),
		UserID:      session.UserID,
	})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
