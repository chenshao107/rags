package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/chenshao/rags/internal/app"
	"github.com/chenshao/rags/internal/client"
	"github.com/chenshao/rags/internal/config"
)

const maxConcurrentSearches = 8

// Server serves read-only search requests for a public rags repository.
type Server struct {
	RepoRoot  string
	Config    config.ProjectConfig
	Settings  config.GlobalSettings
	Endpoint  string
	network   string
	listenAddr string

	sem chan struct{}
}

// New creates a public search server.
func New(repoRoot string, pcfg config.ProjectConfig, settings config.GlobalSettings) (*Server, error) {
	if !pcfg.IsPublic() || pcfg.Public == nil {
		return nil, fmt.Errorf("not a public rags repository")
	}

	endpoint := pcfg.Public.Endpoint
	if endpoint == "" {
		endpoint = config.PublicEndpoint(pcfg.Name)
	}

	network, listenAddr, err := config.ServerListenAddr(endpoint)
	if err != nil {
		return nil, err
	}

	return &Server{
		RepoRoot:   repoRoot,
		Config:     pcfg,
		Settings:   settings,
		Endpoint:   endpoint,
		network:    network,
		listenAddr: listenAddr,
		sem:        make(chan struct{}, maxConcurrentSearches),
	}, nil
}

// Run starts the HTTP server until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if s.network == "unix" {
		if err := os.MkdirAll(config.PublicRunDir, 0755); err != nil {
			return fmt.Errorf("create run dir: %w", err)
		}
		_ = os.Remove(s.listenAddr)
	}

	ln, err := net.Listen(s.network, s.listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s %s: %w", s.network, s.listenAddr, err)
	}
	if s.network == "unix" {
		if err := os.Chmod(s.listenAddr, 0666); err != nil {
			return fmt.Errorf("chmod socket: %w", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/health", s.handleHealth)

	httpServer := &http.Server{Handler: mux}

	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()

	fmt.Printf("rags public server listening on %s (%s %s, repo: %s)\n", s.Endpoint, s.network, s.listenAddr, s.RepoRoot)
	err = httpServer.Serve(ln)
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		http.Error(w, "too many concurrent search requests", http.StatusTooManyRequests)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	var req client.SearchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(req.Queries) == 0 {
		http.Error(w, "no queries provided", http.StatusBadRequest)
		return
	}
	if req.TopK <= 0 {
		req.TopK = 5
	}

	result, err := app.RunSearch(s.Config, s.Settings, app.SearchOptions{
		Queries:    req.Queries,
		TopK:       req.TopK,
		Dedup:      req.Dedup,
		Explore:    req.Explore,
		PathPrefix: req.PathPrefix,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, client.SearchResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, client.SearchResponse{Output: result.Output})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// IsRoot reports whether the current process is running as root.
func IsRoot() bool {
	return os.Geteuid() == 0
}

// RequireRootForPublicWrite returns an error if a public repo write is attempted without root.
func RequireRootForPublicWrite(pcfg config.ProjectConfig) error {
	if !pcfg.IsPublic() {
		return nil
	}
	if pcfg.Public != nil && pcfg.Public.Deploy == config.DeployDocker {
		return nil
	}
	if !IsRoot() {
		return fmt.Errorf("public repository %q is read-only; run with sudo to update", pcfg.Name)
	}
	return nil
}
