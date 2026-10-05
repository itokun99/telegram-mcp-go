// Package mcpserver wires the MCP server, transports, and tool registration.
//
// It is the Go port of the Python boot layer (telegram_mcp/runtime.py and
// runner.py):
//
//   - tool packages register tools with RegisterTool, each carrying a typed
//     input struct from which the MCP SDK derives the tool's JSON schema;
//   - Build constructs the server and applies TELEGRAM_EXPOSED_TOOLS pruning:
//     read-only drops every tool without ReadOnlyHint, a read-only+name,name
//     allowlist re-adds named writes, and an unknown name aborts startup;
//   - every call runs under the per-call timeout (TELEGRAM_TOOL_TIMEOUT_SECONDS
//     via internal/config, default 55s, <= 0 unbounded);
//   - every result content block is stamped audience=["user"] when it carries
//     no audience;
//   - Run serves stdio (default), streamable HTTP (MCP_TRANSPORT=http) or
//     legacy SSE (MCP_TRANSPORT=sse).
//
// Main is the process entry point. Its --dry-run flag prints the served tool
// list as sorted JSON and exits 0 without connecting to Telegram or starting
// a transport; the parity gate and CI diff that output.
package mcpserver
