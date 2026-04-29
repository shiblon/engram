package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "[EXPERIMENTAL] Start an MCP server exposing engram tools over stdio",
	RunE:  runMCP,
}

func runMCP(_ *cobra.Command, _ []string) error {
	s := server.NewMCPServer(
		"engram",
		"0.1.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(false, false),
	)

	registerMCPResources(s)
	registerMCPTools(s)

	return server.ServeStdio(s)
}

func registerMCPResources(s *server.MCPServer) {
	s.AddResource(mcp.Resource{
		URI:         "engram://inject",
		Name:        "Engram session context",
		Description: "Personality, preferences, memories, and recent file activity for this project. Read at session start.",
		MIMEType:    "text/plain",
	}, mcpResourceInject)

	s.AddResource(mcp.Resource{
		URI:         "engram://agentinfo",
		Name:        "Engram agent instructions",
		Description: "How to use engram: memory workflow, tiers, and commands.",
		MIMEType:    "text/plain",
	}, mcpResourceAgentInfo)
}

func mcpResourceInject(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	cwd := effectiveCWD()

	root, err := engram.FindProjectRoot(cwd)
	if err != nil {
		return textResource(req.Params.URI, "{}"), nil
	}

	db, err := engram.OpenProjectDB(ctx, root)
	if err != nil {
		return textResource(req.Params.URI, "{}"), nil
	}
	defer db.Close()

	projectResult, _ := engram.Inject(ctx, db, engram.DefaultInjectSessions)

	var globalResult engram.InjectResult
	if engram.GlobalDBExists() {
		if gdb, err := engram.OpenGlobalDB(ctx); err == nil {
			globalResult, _ = engram.Inject(ctx, gdb, engram.DefaultInjectSessions)
			gdb.Close()
		}
	}

	return textResource(req.Params.URI, engram.InjectContextText(globalResult, projectResult, engram.DefaultInjectSessions)), nil
}

func mcpResourceAgentInfo(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return textResource(req.Params.URI, agentInfoText), nil
}

func textResource(uri, text string) []mcp.ResourceContents {
	return []mcp.ResourceContents{
		mcp.TextResourceContents{URI: uri, MIMEType: "text/plain", Text: text},
	}
}

func registerMCPTools(s *server.MCPServer) {
	// engram_context: no-op tool whose description bootstraps session context
	s.AddTool(mcp.NewTool("engram_context",
		mcp.WithDescription("Engram memory system is active. IMPORTANT: On your very first interaction this session, before responding to the user, read the engram://inject resource (personality, preferences, memories, recently active files) and the engram://agentinfo resource (instructions for using engram). Do this once at session start only. After reading, proceed normally."),
	), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})

	// mem_write: upsert a memory entry
	s.AddTool(mcp.NewTool("mem_write",
		mcp.WithDescription("Write (upsert) a memory entry."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Memory key")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Memory content")),
		mcp.WithString("tier", mcp.Description("Memory tier: invariant, preference, long, short (default: short)")),
		mcp.WithString("cwd", mcp.Description("Project directory (omit for global)")),
		mcp.WithBoolean("global", mcp.Description("Write to global database")),
	), mcpMemWrite)

	// mem_read: read a memory entry
	s.AddTool(mcp.NewTool("mem_read",
		mcp.WithDescription("Read a memory entry by key. Searches all tiers if tier is omitted."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Memory key")),
		mcp.WithString("tier", mcp.Description("Memory tier (optional)")),
		mcp.WithString("cwd", mcp.Description("Project directory (omit for global)")),
		mcp.WithBoolean("global", mcp.Description("Read from global database")),
	), mcpMemRead)

	// mem_list: list memories
	s.AddTool(mcp.NewTool("mem_list",
		mcp.WithDescription("List memories. Omit tier to list all tiers."),
		mcp.WithString("tier", mcp.Description("Memory tier (optional)")),
		mcp.WithString("cwd", mcp.Description("Project directory (omit for global)")),
		mcp.WithBoolean("global", mcp.Description("List from global database")),
	), mcpMemList)

	// mem_search: full-text search
	s.AddTool(mcp.NewTool("mem_search",
		mcp.WithDescription("Full-text search across memories."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
		mcp.WithString("tier", mcp.Description("Limit to tier (optional)")),
		mcp.WithString("cwd", mcp.Description("Project directory (omit for global)")),
		mcp.WithBoolean("global", mcp.Description("Search global database")),
	), mcpMemSearch)

	// mem_delete: delete a memory entry
	s.AddTool(mcp.NewTool("mem_delete",
		mcp.WithDescription("Delete a memory entry."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Memory key")),
		mcp.WithString("tier", mcp.Description("Memory tier (optional, errors if ambiguous)")),
		mcp.WithString("cwd", mcp.Description("Project directory (omit for global)")),
		mcp.WithBoolean("global", mcp.Description("Delete from global database")),
	), mcpMemDelete)
}

// openMCPDB opens the project or global DB based on MCP tool arguments.
func openMCPDB(ctx context.Context, req mcp.CallToolRequest) (*engram.DBHandle, error) {
	global, _ := req.GetArguments()["global"].(bool)
	if global {
		db, err := engram.OpenGlobalDB(ctx)
		if err != nil {
			return nil, err
		}
		path, _ := engram.GlobalDBPath()
		return &engram.DBHandle{DB: db, Path: path}, nil
	}
	cwd, _ := req.GetArguments()["cwd"].(string)
	if cwd == "" {
		cwd = effectiveCWD()
	}
	root, err := engram.FindProjectRoot(cwd)
	if err != nil {
		return nil, fmt.Errorf("no project root found from %s", cwd)
	}
	db, err := engram.OpenProjectDB(ctx, root)
	if err != nil {
		return nil, err
	}
	return &engram.DBHandle{DB: db, Path: engram.DBPath(root)}, nil
}


func mcpMemWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h, err := openMCPDB(ctx, req)
	if err != nil {
		return nil, err
	}
	defer h.DB.Close()

	key, _ := req.GetArguments()["key"].(string)
	content, _ := req.GetArguments()["content"].(string)
	tier := engram.TierShort
	if t, ok := req.GetArguments()["tier"].(string); ok && t != "" {
		tier = engram.Tier(t)
	}

	if err := engram.WriteMemory(ctx, h.DB, engram.Memory{
		Tier:    tier,
		Key:     key,
		Content: content,
	}); err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(fmt.Sprintf("stored %s/%s", tier, key)), nil
}

func mcpMemRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h, err := openMCPDB(ctx, req)
	if err != nil {
		return nil, err
	}
	defer h.DB.Close()

	key, _ := req.GetArguments()["key"].(string)
	tierStr, tierSet := req.GetArguments()["tier"].(string)

	if !tierSet || tierStr == "" {
		matches, err := engram.FindMemoryByKey(ctx, h.DB, key)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("not found: %s", key)), nil
		}
		var sb strings.Builder
		for _, m := range matches {
			fmt.Fprintf(&sb, "[%s/%s]\n%s\n\n", m.Tier, m.Key, m.Content)
		}
		return mcp.NewToolResultText(sb.String()), nil
	}

	m, err := engram.ReadMemory(ctx, h.DB, engram.Tier(tierStr), key)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return mcp.NewToolResultText(fmt.Sprintf("not found: %s/%s", tierStr, key)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("[%s/%s]\n%s", m.Tier, m.Key, m.Content)), nil
}

func mcpMemList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h, err := openMCPDB(ctx, req)
	if err != nil {
		return nil, err
	}
	defer h.DB.Close()

	tierStr, tierSet := req.GetArguments()["tier"].(string)

	var memories []engram.Memory
	if tierSet && tierStr != "" {
		memories, err = engram.ListMemories(ctx, h.DB, engram.Tier(tierStr))
	} else {
		for _, t := range []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort} {
			ms, err := engram.ListMemories(ctx, h.DB, t)
			if err != nil {
				return nil, err
			}
			memories = append(memories, ms...)
		}
	}
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(memories)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(out)), nil
}

func mcpMemSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h, err := openMCPDB(ctx, req)
	if err != nil {
		return nil, err
	}
	defer h.DB.Close()

	query, _ := req.GetArguments()["query"].(string)
	tierStr, _ := req.GetArguments()["tier"].(string)

	results, err := engram.SearchMemories(ctx, h.DB, query, engram.Tier(tierStr))
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(out)), nil
}

func mcpMemDelete(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h, err := openMCPDB(ctx, req)
	if err != nil {
		return nil, err
	}
	defer h.DB.Close()

	key, _ := req.GetArguments()["key"].(string)
	tierStr, tierSet := req.GetArguments()["tier"].(string)

	if !tierSet || tierStr == "" {
		matches, err := engram.FindMemoryByKey(ctx, h.DB, key)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("not found: %s", key)
		}
		if len(matches) > 1 {
			var tiers []string
			for _, m := range matches {
				tiers = append(tiers, string(m.Tier))
			}
			return nil, fmt.Errorf("ambiguous: %q found in tiers: %s", key, strings.Join(tiers, ", "))
		}
		if err := engram.DeleteMemory(ctx, h.DB, matches[0].Tier, key); err != nil {
			return nil, err
		}
		return mcp.NewToolResultText(fmt.Sprintf("deleted %s/%s", matches[0].Tier, key)), nil
	}

	if err := engram.DeleteMemory(ctx, h.DB, engram.Tier(tierStr), key); err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(fmt.Sprintf("deleted %s/%s", tierStr, key)), nil
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
