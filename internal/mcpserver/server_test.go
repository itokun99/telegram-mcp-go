package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/itokun99/telegram-mcp-go/internal/config"
)

// validEnv mirrors config_test.validEnv: the minimal environment that passes
// config.Load (credentials plus one default session).
func validEnv() map[string]string {
	return map[string]string{
		"TELEGRAM_API_ID":         "123456",
		"TELEGRAM_API_HASH":       "0123456789abcdef0123456789abcdef",
		"TELEGRAM_SESSION_STRING": "sessionA",
	}
}

type echoInput struct {
	Text string `json:"text" jsonschema:"text to echo back"`
}

func fakeReadTool() *Tool {
	return newTool("read_tool", "fake read tool", ToolOptions{Title: "Read", ReadOnly: true, OpenWorld: true},
		func(_ context.Context, in echoInput) (string, error) { return "read:" + in.Text, nil })
}

func fakeWriteTool() *Tool {
	return newTool("write_tool", "fake write tool", ToolOptions{Title: "Write", Destructive: true, OpenWorld: true},
		func(context.Context, struct{}) (string, error) { return "write", nil })
}

// registryWithFakes registers the write tool first so sorted-output
// assertions are meaningful.
func registryWithFakes() *Registry {
	reg := NewRegistry()
	reg.Register(fakeWriteTool())
	reg.Register(fakeReadTool())
	return reg
}

// connectClient opens an in-memory MCP session against srv.
func connectClient(t *testing.T, srv *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.MCP.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func servedToolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("result has no content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type %T, want *mcp.TextContent", res.Content[0])
	}
	return text.Text
}

func TestReadOnlyPruningRemovesUnlabeledTool(t *testing.T) {
	srv, err := Build(registryWithFakes(), Options{ExposedMode: "read-only"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if names := srv.ToolNames(); slices.Contains(names, "write_tool") {
		t.Fatalf("read-only served write_tool: %v", names)
	}

	cs := connectClient(t, srv)
	if names := servedToolNames(t, cs); slices.Contains(names, "write_tool") {
		t.Fatalf("MCP server still lists write_tool: %v", names)
	}
}

func TestReadOnlyPruningKeepsReadOnlyTool(t *testing.T) {
	srv, err := Build(registryWithFakes(), Options{ExposedMode: "read-only"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{"read_tool"}
	if got := srv.ToolNames(); !slices.Equal(got, want) {
		t.Fatalf("served tool names = %v, want %v", got, want)
	}
	cs := connectClient(t, srv)
	if got := servedToolNames(t, cs); !slices.Equal(got, want) {
		t.Fatalf("MCP served tools = %v, want %v", got, want)
	}
}

func TestReadOnlyAllowlistReaddsNamedWriteTool(t *testing.T) {
	srv, err := Build(registryWithFakes(), Options{ExposedMode: "read-only", ExposedAllowlist: []string{"write_tool"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{"read_tool", "write_tool"}
	if got := srv.ToolNames(); !slices.Equal(got, want) {
		t.Fatalf("served tool names = %v, want %v", got, want)
	}
}

func TestUnknownAllowlistNameAbortsBuild(t *testing.T) {
	_, err := Build(registryWithFakes(), Options{ExposedMode: "read-only", ExposedAllowlist: []string{"send_mesage"}})
	if err == nil || !strings.Contains(err.Error(), "send_mesage") {
		t.Fatalf("Build error = %v, want unknown-tool abort naming send_mesage", err)
	}
}

func TestInvalidExposedModeAbortsBuild(t *testing.T) {
	_, err := Build(registryWithFakes(), Options{ExposedMode: "everything"})
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_EXPOSED_TOOLS") {
		t.Fatalf("Build error = %v, want invalid-mode abort", err)
	}
}

func TestDuplicateToolNameAbortsBuild(t *testing.T) {
	reg := NewRegistry()
	reg.Register(fakeReadTool())
	reg.Register(fakeReadTool())
	_, err := Build(reg, Options{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Build error = %v, want duplicate-tool abort", err)
	}
}

func TestTimeoutCancelsSleepingHandler(t *testing.T) {
	cancelled := make(chan struct{})
	reg := NewRegistry()
	reg.Register(newTool("sleep_tool", "sleeps until cancelled", ToolOptions{ReadOnly: true},
		func(ctx context.Context, _ struct{}) (string, error) {
			<-ctx.Done()
			close(cancelled)
			return "", ctx.Err()
		}))
	srv, err := Build(reg, Options{ToolTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cs := connectClient(t, srv)

	start := time.Now()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sleep_tool"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want timeout error result, got %+v", res)
	}
	text := textOf(t, res)
	if !strings.Contains(text, "GEN-TIMEOUT") || !strings.Contains(text, "timed out after 0.1s") {
		t.Fatalf("timeout text = %q", text)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout wrapper did not bound the call: %v", elapsed)
	}

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler context was not cancelled by the timeout")
	}

	annotations := res.Content[0].(*mcp.TextContent).Annotations
	if annotations == nil || len(annotations.Audience) != 1 || annotations.Audience[0] != "user" {
		t.Fatalf("timeout result audience = %+v, want [user]", annotations)
	}
}

func TestToolTimeoutEnvOverrideReachesWrapper(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_TOOL_TIMEOUT_SECONDS"] = "0.05"
	cfg, err := config.Load(config.NewInMemory(env))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.ToolTimeoutSeconds != 0.05 {
		t.Fatalf("config.ToolTimeoutSeconds = %v, want 0.05", cfg.ToolTimeoutSeconds)
	}

	reg := NewRegistry()
	reg.Register(newTool("sleep_tool", "sleeps until cancelled", ToolOptions{ReadOnly: true},
		func(ctx context.Context, _ struct{}) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}))
	srv, err := Build(reg, OptionsFromConfig(cfg))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cs := connectClient(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sleep_tool"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError || !strings.Contains(textOf(t, res), "timed out after 0.05s") {
		t.Fatalf("want env-override timeout text, got %q", textOf(t, res))
	}
}

func TestOptionsFromConfigMapsFields(t *testing.T) {
	cfg := &config.Config{
		Transport:          "http",
		Host:               "0.0.0.0",
		Port:               9000,
		ExposedMode:        "read-only",
		ExposedAllowlist:   []string{"write_tool"},
		ToolTimeoutSeconds: 3.5,
	}
	opts := OptionsFromConfig(cfg)
	if opts.Transport != "http" || opts.Host != "0.0.0.0" || opts.Port != 9000 {
		t.Fatalf("transport mapping = %q %q %d", opts.Transport, opts.Host, opts.Port)
	}
	if opts.ToolTimeout != 3500*time.Millisecond {
		t.Fatalf("ToolTimeout = %v, want 3.5s", opts.ToolTimeout)
	}
	if opts.ExposedMode != "read-only" || !slices.Equal(opts.ExposedAllowlist, []string{"write_tool"}) {
		t.Fatalf("exposure mapping = %q %v", opts.ExposedMode, opts.ExposedAllowlist)
	}

	unlimited := OptionsFromConfig(&config.Config{ToolTimeoutUnlimited: true, ToolTimeoutSeconds: 0})
	if unlimited.ToolTimeout != 0 {
		t.Fatalf("unlimited ToolTimeout = %v, want 0", unlimited.ToolTimeout)
	}
}

func TestToolResultAudienceStampedWhenAbsent(t *testing.T) {
	srv, err := Build(registryWithFakes(), Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cs := connectClient(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "read_tool",
		Arguments: map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %q", textOf(t, res))
	}
	if got := textOf(t, res); got != "read:hello" {
		t.Fatalf("text = %q, want read:hello", got)
	}
	annotations := res.Content[0].(*mcp.TextContent).Annotations
	if annotations == nil || len(annotations.Audience) != 1 || annotations.Audience[0] != "user" {
		t.Fatalf("audience = %+v, want [user]", annotations)
	}
}

func TestStampAudiencePreservesExisting(t *testing.T) {
	existing := &mcp.TextContent{Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}}
	plain := &mcp.TextContent{}
	stampUserAudience(&mcp.CallToolResult{Content: []mcp.Content{existing, plain}})

	if got := existing.Annotations.Audience; len(got) != 1 || got[0] != "assistant" {
		t.Fatalf("existing audience overwritten: %v", got)
	}
	if got := plain.Annotations; got == nil || len(got.Audience) != 1 || got.Audience[0] != "user" {
		t.Fatalf("plain block audience = %+v, want [user]", got)
	}
}
