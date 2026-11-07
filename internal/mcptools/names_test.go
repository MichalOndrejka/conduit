package mcptools

import (
	"slices"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestToolNamesListsRegisteredTools(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0")
	s.AddTool(mcp.NewTool("search_b"), nil)
	s.AddTool(mcp.NewTool("search_a"), nil)
	if got := ToolNames(s); !slices.Equal(got, []string{"search_a", "search_b"}) {
		t.Errorf("ToolNames = %v", got)
	}
}
