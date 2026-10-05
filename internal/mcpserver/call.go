package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// invokeTool runs one tool call under the configured per-call timeout.
//
// timeout <= 0 means unbounded (TELEGRAM_TOOL_TIMEOUT_SECONDS <= 0). A
// positive timeout cancels the handler context and returns the parity
// GEN-TIMEOUT error result even when the handler ignores cancellation, so a
// wedged Telegram request cannot hang the client.
func invokeTool(ctx context.Context, timeout time.Duration, call func(context.Context) (*mcp.CallToolResult, error)) *mcp.CallToolResult {
	if timeout <= 0 {
		return runGuarded(ctx, call)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan *mcp.CallToolResult, 1)
	go func() { done <- runGuarded(ctx, call) }()

	select {
	case res := <-done:
		return res
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return timeoutResult(timeout)
		}
		return toolError("Telegram MCP tool call cancelled.")
	}
}

// runGuarded runs call, converting a handler panic into a tool error so one
// bad tool cannot kill the process.
func runGuarded(ctx context.Context, call func(context.Context) (*mcp.CallToolResult, error)) (res *mcp.CallToolResult) {
	defer func() {
		if r := recover(); r != nil {
			res = toolError(fmt.Sprintf("Telegram MCP tool panic: %v", r))
		}
	}()
	res, err := call(ctx)
	if err != nil {
		return toolError(err.Error())
	}
	return res
}

// timeoutResult mirrors runtime.py's timeout message (code GEN-TIMEOUT).
// %g keeps the Python formatting ("55s", "0.01s").
func timeoutResult(timeout time.Duration) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(
			"Telegram MCP tool timed out after %gs (code: GEN-TIMEOUT). "+
				"Completion is unknown; a write may already have succeeded. "+
				"Check destination state before retrying non-idempotent operations.",
			timeout.Seconds())}},
	}
}

// textResult wraps a tool's string result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// toolError wraps a tool error string as an MCP tool error.
func toolError(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// userAudience is the fixed audience stamped onto every result block that
// carries none (runtime._USER_AUDIENCE).
func userAudience() *mcp.Annotations { return &mcp.Annotations{Audience: []mcp.Role{"user"}} }

// stampUserAudience marks every content block of res with audience=["user"]
// unless the block already declares an audience, so MCP clients treat tool
// output as user data rather than model instructions.
func stampUserAudience(res *mcp.CallToolResult) {
	if res == nil {
		return
	}
	for _, block := range res.Content {
		switch c := block.(type) {
		case *mcp.TextContent:
			stampAudience(&c.Annotations)
		case *mcp.ImageContent:
			stampAudience(&c.Annotations)
		case *mcp.AudioContent:
			stampAudience(&c.Annotations)
		case *mcp.ResourceLink:
			stampAudience(&c.Annotations)
		case *mcp.EmbeddedResource:
			stampAudience(&c.Annotations)
		}
	}
}

func stampAudience(a **mcp.Annotations) {
	if *a == nil {
		*a = userAudience()
		return
	}
	if len((*a).Audience) == 0 {
		(*a).Audience = []mcp.Role{"user"}
	}
}
