package web

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/uykb/MartinStrategy-Hedging/internal/exchange"
	"github.com/uykb/MartinStrategy-Hedging/internal/storage"
	"github.com/uykb/MartinStrategy-Hedging/internal/strategy"
)

//go:embed static/*
var staticFiles embed.FS

// Server holds dependencies for the web interface
type Server struct {
	strategies   []*strategy.MartingaleStrategy
	storage      *storage.Database
	exchange     *exchange.BinanceClient
	router       *gin.Engine
	statusCache  *statusCache
}

// NewServer creates a new web server instance
func NewServer(strategies []*strategy.MartingaleStrategy, st *storage.Database, ex *exchange.BinanceClient) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	s := &Server{
		strategies:  strategies,
		storage:     st,
		exchange:    ex,
		router:      r,
		statusCache: &statusCache{},
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// API Routes (must be registered before static catch-all)
	api := s.router.Group("/api")
	{
		api.GET("/status", s.handleStatus)
		api.GET("/orders", s.handleOrders)
		api.GET("/pnl", s.handlePnL)
	}

	// Static Files - serve index.html for root and SPA fallback
	staticFS, _ := fs.Sub(staticFiles, "static")
	s.router.GET("/", func(c *gin.Context) {
		c.FileFromFS("/", http.FS(staticFS))
	})

	// Favicon - return 204 No Content to avoid 404 noise
	s.router.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
}

// Start runs the web server
func (s *Server) Start(addr string) error {
	return s.router.Run(addr)
}
