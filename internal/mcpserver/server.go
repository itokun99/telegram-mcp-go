package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is the server version, overwritable at link time:
//
//	go build -ldflags "-X github.com/itokun99/telegram-mcp-go/internal/mcpserver.Version=v1.2.3"
//
// When left at "dev" the module build info supplies the version.
var Version = "dev"

// Options configures server construction. OptionsFromConfig maps the parsed
// environment onto it.
type Options struct {
	// Name is the MCP server name (default "telegram-mcp-go").
	Name string
	// Version is the advertised server version (default: build version).
	Version string
	// Transport is "stdio" (default), "http" (streamable HTTP) or "sse".
	Transport string
	// Host and Port bind the HTTP transports (default 127.0.0.1:8765).
	Host string
	Port int
	// ToolTimeout caps one tool call; 0 means unbounded
	// (TELEGRAM_TOOL_TIMEOUT_SECONDS <= 0, else the config default of 55s).
	ToolTimeout time.Duration
	// ExposedMode is TELEGRAM_EXPOSED_TOOLS: "" or "all" serves every tool,
	// "read-only" serves only ReadOnly tools plus ExposedAllowlist.
	ExposedMode string
	// ExposedAllowlist re-adds named write tools under read-only.
	ExposedAllowlist []string
}

func (o Options) withDefaults() Options {
	if o.Name == "" {
		o.Name = "telegram-mcp-go"
	}
	if o.Version == "" {
		o.Version = buildVersion()
	}
	if o.Transport == "" {
		o.Transport = "stdio"
	}
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	if o.Port == 0 {
		o.Port = 8765
	}
	if o.ExposedMode == "" {
		o.ExposedMode = "all"
	}
	return o
}

func buildVersion() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return Version
}

// Server is the built MCP server.
type Server struct {
	// MCP is the underlying SDK server.
	MCP *mcp.Server

	opts  Options
	tools []*Tool
}

// Build constructs the MCP server from the registry's tools, applying
// TELEGRAM_EXPOSED_TOOLS pruning. It fails loudly on a duplicate tool name,
// an unknown exposure mode, an unknown allowlist name, or an unknown
// transport.
func Build(reg *Registry, opts Options) (*Server, error) {
	opts = opts.withDefaults()
	switch opts.Transport {
	case "stdio", "http", "sse":
	default:
		return nil, fmt.Errorf("invalid MCP_TRANSPORT '%s'. Expected one of: stdio, http, sse.", opts.Transport)
	}

	tools := reg.tools()
	seen := make(map[string]bool, len(tools))
	for _, t := range tools {
		if seen[t.Name] {
			return nil, fmt.Errorf("duplicate tool %q registered", t.Name)
		}
		seen[t.Name] = true
	}
	served, err := applyExposure(tools, opts.ExposedMode, opts.ExposedAllowlist)
	if err != nil {
		return nil, err
	}
	sort.Slice(served, func(i, j int) bool { return served[i].Name < served[j].Name })

	s := mcp.NewServer(&mcp.Implementation{Name: opts.Name, Version: opts.Version}, nil)
	for _, t := range served {
		t.add(s, opts.ToolTimeout)
	}
	return &Server{MCP: s, opts: opts, tools: served}, nil
}

// Options returns the effective (defaulted) options.
func (s *Server) Options() Options { return s.opts }

// ToolNames returns the sorted names of the served (post-pruning) tools.
func (s *Server) ToolNames() []string {
	names := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		names = append(names, t.Name)
	}
	return names
}

// Run serves the server on the configured transport until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	switch s.opts.Transport {
	case "http":
		// Stateless mirrors FastMCP(stateless_http=True): long-lived clients
		// survive a server restart instead of losing their session ID.
		handler := mcp.NewStreamableHTTPHandler(
			func(*http.Request) *mcp.Server { return s.MCP },
			&mcp.StreamableHTTPOptions{Stateless: true},
		)
		return s.serveHTTP(ctx, handler)
	case "sse":
		handler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return s.MCP }, nil)
		return s.serveHTTP(ctx, handler)
	default:
		return s.MCP.Run(ctx, &mcp.StdioTransport{})
	}
}

func (s *Server) serveHTTP(ctx context.Context, handler http.Handler) error {
	addr := net.JoinHostPort(s.opts.Host, strconv.Itoa(s.opts.Port))
	srv := &http.Server{Addr: addr, Handler: handler}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	}
}
