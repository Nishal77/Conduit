// Package api implements Conduit's management REST API — CRUD for
// tenants, API keys, MCP servers, rate limits, and audit log queries, all
// under /api/v1 on its own port (:8081 by default) so a network policy can
// block it from agent traffic entirely (spec/10-api.md §1).
package api

import (
	"net/http"

	"github.com/conduit-oss/conduit/internal/api/handlers"
	apimiddleware "github.com/conduit-oss/conduit/internal/api/middleware"
	"github.com/conduit-oss/conduit/internal/auth"
	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
)

// Server is the management API HTTP server. It implements http.Handler
// directly so callers wire it into an *http.Server the same way they do
// the proxy.
type Server struct {
	router http.Handler
}

// New builds the chi router with every handler and middleware wired in.
// keyValidator authenticates requests (see apimiddleware.Auth for why API
// keys, not JWTs, until Phase 5).
func New(cfg *config.Config, stores *store.Stores, keyValidator *auth.APIKeyValidator, log zerolog.Logger) *Server {
	h := handlers.New(stores, log)
	return &Server{router: buildRouter(cfg, h, keyValidator, log)}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func buildRouter(cfg *config.Config, h *handlers.Handlers, keyValidator *auth.APIKeyValidator, log zerolog.Logger) http.Handler {
	r := chi.NewRouter()

	// Global middleware (runs for every request).
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(chiZerologLogger(log))
	r.Use(middleware.Recoverer)
	r.Use(apimiddleware.CORS(cfg.Server.CORSOrigins))
	r.Use(apimiddleware.JSONContentType)

	r.Route("/api/v1", func(r chi.Router) {
		// Health — no auth required.
		r.Get("/health", h.Health.Get)

		// Everything else requires a valid API key.
		r.Group(func(r chi.Router) {
			r.Use(apimiddleware.Auth(keyValidator))

			r.Route("/tenants", func(r chi.Router) {
				r.Get("/", h.Tenants.List)
				r.Post("/", h.Tenants.Create)
				r.Get("/{tenantID}", h.Tenants.Get)
				r.Patch("/{tenantID}", h.Tenants.Update)
				r.Delete("/{tenantID}", h.Tenants.Delete)
			})

			r.Route("/api-keys", func(r chi.Router) {
				r.Get("/", h.APIKeys.List)
				r.Post("/", h.APIKeys.Create)
				r.Delete("/{keyID}", h.APIKeys.Revoke)
			})

			r.Route("/servers", func(r chi.Router) {
				r.Get("/", h.Servers.List)
				r.Post("/", h.Servers.Create)
				r.Get("/{serverID}", h.Servers.Get)
				r.Patch("/{serverID}", h.Servers.Update)
				r.Delete("/{serverID}", h.Servers.Delete)
				r.Get("/{serverID}/health", h.Servers.Health)
			})

			r.Route("/rate-limits", func(r chi.Router) {
				r.Get("/", h.RateLimits.List)
				r.Put("/", h.RateLimits.Upsert)
				r.Delete("/{id}", h.RateLimits.Delete)
			})

			r.Route("/audit", func(r chi.Router) {
				r.Get("/events", h.Audit.Query)
				r.Get("/export", h.Audit.Export)
				r.Get("/stream", h.Audit.Stream)
			})

			// /webhooks, /plugins, and /oauth/applications are not wired
			// up yet — see the package doc comment on internal/api/handlers
			// for why (their tables land in Phases 5 and 6).
		})
	})

	r.Get("/api/openapi.json", ServeOpenAPI)

	return r
}

// chiZerologLogger adapts chi's middleware.Logger interface to zerolog, so
// the management API's access log matches the proxy's structured JSON
// format instead of chi's default plain-text one.
func chiZerologLogger(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info().
				Str("request_id", middleware.GetReqID(r.Context())).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", ww.Status()).
				Int("bytes", ww.BytesWritten()).
				Msg("management api request")
		})
	}
}
