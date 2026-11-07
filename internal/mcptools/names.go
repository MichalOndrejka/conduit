package mcptools

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ToolNames lists the tools registered on s, sorted. mcp-go has no accessor
// for them, so it asks the server the way a client would: tools/list.
func ToolNames(s *server.MCPServer) []string {
	reply := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	resp, ok := reply.(mcp.JSONRPCResponse)
	if !ok {
		return nil
	}
	list, ok := resp.Result.(mcp.ListToolsResult)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(list.Tools))
	for _, t := range list.Tools {
		names = append(names, t.Name)
	}
	return names
}
