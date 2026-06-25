package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"dns-edge/config"
	"dns-edge/internal/iface"
)

// Server wraps gin and http.Server for the hot-update HTTP API.
type Server struct {
	pg           iface.RecordStore
	store        iface.ZoneStore
	log          *zap.Logger
	goedgeSecret string
	edgeDNSAuth  *edgeDNSAuth
	httpSrv      *http.Server
}

// New creates a Server and registers all routes. Call Start to begin serving.
func New(cfg config.APIConfig, pg iface.RecordStore, zs iface.ZoneStore, log *zap.Logger) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	s := &Server{
		pg:           pg,
		store:        zs,
		log:          log,
		goedgeSecret: cfg.GoEdgeSecret,
		edgeDNSAuth:  newEdgeDNSAuth(cfg.EdgeDNSKeyID, cfg.EdgeDNSKeySecret),
		httpSrv: &http.Server{
			Addr:    cfg.Listen,
			Handler: r,
		},
	}

	v1 := r.Group("/api/v1")
	{
		v1.GET("/domains", s.listDomains)
		v1.POST("/domains", s.createDomain)
		v1.DELETE("/domains/:domain", s.deleteDomain)

		v1.GET("/domains/:domain/records", s.listRecords)
		v1.POST("/domains/:domain/records", s.createRecord)
		v1.PUT("/domains/:domain/records/:id", s.updateRecord)
		v1.DELETE("/domains/:domain/records/:id", s.deleteRecord)
	}

	// Prometheus metrics endpoint — consumed by Prometheus scraper.
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Health endpoints — used as liveness/readiness probes.
	r.GET("/healthz", s.healthz)

	// GoEdge customHTTP DNS provider endpoint.
	r.POST("/goedge/dns", s.goedgeProvider)

	// GoEdge edgeDNSAPI provider endpoints (14 endpoints across NS*Service).
	s.registerEdgeDNSRoutes(r)

	return s
}

// Start begins listening in a background goroutine.
func (s *Server) Start() error {
	go func() {
		s.log.Info("API listening", zap.String("addr", s.httpSrv.Addr))
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("API server stopped", zap.Error(err))
		}
	}()
	return nil
}

// Shutdown drains in-flight requests gracefully.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// ServeHTTP implements http.Handler, allowing the server to be driven by
// httptest.ResponseRecorder in tests without binding to a real port.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.httpSrv.Handler.ServeHTTP(w, r)
}

// healthz returns 200 {"status":"ok"} unconditionally.
// Suitable for liveness probes; a non-200 only occurs if the process is dead.
func (s *Server) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
