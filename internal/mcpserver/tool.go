package mcpserver

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handler handles one tool call. The typed input is unmarshaled and
// validated by the MCP SDK against the JSON schema inferred from In.
//
// PORT CONVENTION: handlers do not raise. They return the error text as the
// tool result (mirroring the Python log_and_format_error funnel). A non-nil
// error is still served as an MCP tool error (IsError), never as a protocol
// error.
type Handler[In any] func(ctx context.Context, in In) (string, error)

// ToolOptions carries the MCP safety annotations of one tool.
//
// The Python tools always set annotations explicitly
// (telegram_mcp/tools/*.py); ToolOptions mirrors that.
type ToolOptions struct {
	// Title is the human-readable tool title (ToolAnnotations(title=...)).
	Title string
	// ReadOnly sets readOnlyHint. TELEGRAM_EXPOSED_TOOLS=read-only keeps
	// exactly the tools with ReadOnly true plus the +name allowlist.
	ReadOnly bool
	// Destructive sets destructiveHint (writes that can destroy state).
	Destructive bool
	// Idempotent sets idempotentHint.
	Idempotent bool
	// OpenWorld sets openWorldHint. Every Telegram tool talks to an open
	// world, so the ported tools set this.
	OpenWorld bool
}

func (o ToolOptions) annotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           o.Title,
		ReadOnlyHint:    o.ReadOnly,
		DestructiveHint: &o.Destructive,
		IdempotentHint:  o.Idempotent,
		OpenWorldHint:   &o.OpenWorld,
	}
}

// Tool is one registered tool.
type Tool struct {
	Name        string
	Description string
	Options     ToolOptions

	// add binds the tool to a server. The closure captures the typed
	// handler, which keeps Registry free of type parameters (Go methods
	// cannot declare their own).
	add func(s *mcp.Server, timeout time.Duration)
}

// RegisterTool registers a tool in the process-wide DefaultRegistry. Tool
// packages call it, typically from their init functions; Main builds the
// server from that registry.
//
// In is the tool's input struct: the SDK derives the tool's JSON schema from
// it (property names from `json` tags, descriptions from `jsonschema` tags).
func RegisterTool[In any](name, description string, opts ToolOptions, h Handler[In]) {
	DefaultRegistry.Register(newTool(name, description, opts, h))
}

// newTool wires a typed handler into a Tool descriptor.
func newTool[In any](name, description string, opts ToolOptions, h Handler[In]) *Tool {
	return &Tool{
		Name:        name,
		Description: description,
		Options:     opts,
		add: func(s *mcp.Server, timeout time.Duration) {
			tool := &mcp.Tool{
				Name:        name,
				Description: description,
				Annotations: opts.annotations(),
			}
			mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
				res := invokeTool(ctx, timeout, func(callCtx context.Context) (*mcp.CallToolResult, error) {
					text, err := h(callCtx, in)
					if err != nil {
						return toolError(err.Error()), nil
					}
					return textResult(text), nil
				})
				stampUserAudience(res)
				return res, nil, nil
			})
		},
	}
}

// ContentHandler handles one tool call whose result is explicit MCP content
// blocks rather than a single text string. It is the multimodal escape hatch
// for tools that return image data, mirroring the Python tools that return an
// Image content item (media.py open_photo / get_photo_sheet).
type ContentHandler[In any] func(ctx context.Context, in In) ([]mcp.Content, error)

// RegisterContentTool registers a content-returning tool in the process-wide
// DefaultRegistry. Everything else matches RegisterTool.
func RegisterContentTool[In any](name, description string, opts ToolOptions, h ContentHandler[In]) {
	DefaultRegistry.Register(newContentTool(name, description, opts, h))
}

// newContentTool wires a content-returning handler into a Tool descriptor.
func newContentTool[In any](name, description string, opts ToolOptions, h ContentHandler[In]) *Tool {
	return &Tool{
		Name:        name,
		Description: description,
		Options:     opts,
		add: func(s *mcp.Server, timeout time.Duration) {
			tool := &mcp.Tool{
				Name:        name,
				Description: description,
				Annotations: opts.annotations(),
			}
			mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
				res := invokeTool(ctx, timeout, func(callCtx context.Context) (*mcp.CallToolResult, error) {
					blocks, err := h(callCtx, in)
					if err != nil {
						return toolError(err.Error()), nil
					}
					if blocks == nil {
						blocks = []mcp.Content{}
					}
					return &mcp.CallToolResult{Content: blocks}, nil
				})
				stampUserAudience(res)
				return res, nil, nil
			})
		},
	}
}

// Registry holds registered tools in registration order.
type Registry struct {
	mu    sync.Mutex
	order []*Tool
}

// NewRegistry returns an empty tool registry.
func NewRegistry() *Registry { return &Registry{} }

// DefaultRegistry receives every RegisterTool call of the process.
var DefaultRegistry = NewRegistry()

// Register adds t to the registry. Registering the same name twice keeps
// both entries; Build rejects the duplicate loudly.
func (r *Registry) Register(t *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, t)
}

// tools returns a snapshot of the registered tools in registration order.
func (r *Registry) tools() []*Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Tool(nil), r.order...)
}

// Names returns the registered tool names, sorted.
func (r *Registry) Names() []string {
	tools := r.tools()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

// RegisteredToolNames returns the sorted names of every tool registered in
// the process-wide registry. The tool-surface parity diff enumerates this.
func RegisteredToolNames() []string { return DefaultRegistry.Names() }
