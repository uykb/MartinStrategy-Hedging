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
	strategies []*strategy.MartingaleStrategy
	storage    *storage.Database
	exchange   *exchange.BinanceClient
	router     *gin.Engine
}

// NewServer creates a new web server instance
func NewServer(strategies []*strategy.MartingaleStrategy, st *storage.Database, ex *exchange.BinanceClient) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	s := &Server{
		strategies: strategies,
		storage:    st,
		exchange:   ex,
		router:     r,
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// API Routes
	api := s.router.Group("/api")
	{
		api.GET("/status", s.handleStatus)
		api.GET("/orders", s.handleOrders)
		api.GET("/pnl", s.handlePnL)
	}

	// Static Files
	staticFS, _ := fs.Sub(staticFiles, "static")
	s.router.StaticFS("/", http.FS(staticFS))
}

// Start runs the web server
func (s *Server) Start(addr string) error {
	return s.router.Run(addr)
}
