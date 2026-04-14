package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/config"
	"github.com/ChaitanyaPinapaka/skep/internal/daemon"
	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/registry"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
)

// stdoutMu serializes all writes to stdout so that tool responses and
// in-flight progress notifications do not interleave. Any code that writes
// to os.Stdout in this package must take this lock first.
var stdoutMu sync.Mutex

// JSON-RPC 2.0 types
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP protocol types
type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Server is the MCP stdio server.
type Server struct {
	root    string
	skepDir string
	store   *index.Store
}

// Run starts the MCP server on stdio. Blocks until stdin closes.
func Run(root, skepDir string) error {
	dbPath := filepath.Join(skepDir, "index.db")

	// Ensure index is fresh
	index.EnsureFresh(root, skepDir)

	store, err := index.OpenStore(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	s := &Server{root: root, skepDir: skepDir, store: store}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			writeResponse(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}

		resp := s.handle(req)
		if resp.ID != nil || resp.Error != nil {
			writeResponse(resp)
		}
		// Skip writing for notifications (no ID)
	}

	return scanner.Err()
}

func (s *Server) handle(req request) response {
	// Notifications have no ID — don't send a response
	if req.ID == nil {
		// Silently accept notifications (initialized, cancelled, etc.)
		return response{}
	}

	switch req.Method {
	case "initialize":
		return s.handleInit(req)
	case "ping":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	default:
		return response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: fmt.Sprintf("unknown method: %s", req.Method)},
		}
	}
}

func (s *Server) handleInit(req request) response {
	return response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "skep",
				"version": "0.1.0",
			},
		},
	}
}

func (s *Server) handleToolsList(req request) response {
	tools := []toolDef{
		{
			Name:        "get_overview",
			Description: "Get a high-level overview of the codebase: file count, symbol count, top symbols ranked by importance, active tasks, and peer repos in the same workspace.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "search_symbols",
			Description: "Full-text search over symbol names, signatures, and doc comments. Returns matching functions, types, classes with file locations. Use FTS5 syntax: prefix (auth*), boolean (handler OR service), exclusion (export NOT test). Doc comments are omitted by default to keep responses small — pass verbose=true when you need them.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":   map[string]interface{}{"type": "string", "description": "Search query (FTS5 syntax supported)"},
					"limit":   map[string]interface{}{"type": "integer", "description": "Max results (default 20)"},
					"verbose": map[string]interface{}{"type": "boolean", "description": "Include doc_comment field (default false)"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "get_call_graph",
			Description: "Get callers and callees of a symbol by ID. Shows who calls this function and what it calls.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"symbol_id": map[string]interface{}{"type": "string", "description": "Symbol ID (from search_symbols results)"},
				},
				"required": []string{"symbol_id"},
			},
		},
		{
			Name:        "get_file_context",
			Description: "Get all symbols in a file with their signatures, kinds, and line numbers. Faster than reading the file. Doc comments are omitted by default — pass verbose=true when you need them.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string", "description": "Relative file path (e.g., internal/api/router.go)"},
					"verbose":   map[string]interface{}{"type": "boolean", "description": "Include doc_comment field (default false)"},
				},
				"required": []string{"file_path"},
			},
		},
		{
			Name:        "list_tasks",
			Description: "List all tasks in this repo with ID, name, status, classification, and branch.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "create_task",
			Description: "Create a new task in this repo. Returns task ID and dedup result if duplicate.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"description": map[string]interface{}{"type": "string", "description": "Task description"},
				},
				"required": []string{"description"},
			},
		},
		{
			Name:        "show_task",
			Description: "Fetch a task in this repo by id. Returns the full task object: status, classification, plan text, session id, branch, token usage, result, and any clarify questions when status is pending_clarification. Use this to read back what create_task produced or to poll a task's state inside the classifier loop.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "integer", "description": "Task ID from create_task"},
				},
				"required": []string{"task_id"},
			},
		},
		{
			Name:        "approve_task",
			Description: "Approve a local task so the daemon can pick it up for execution. Only valid on tasks in 'pending', 'classified', or 'created' status. Use this after reading a plan from show_task and deciding the plan is acceptable.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "integer", "description": "Task ID to approve"},
				},
				"required": []string{"task_id"},
			},
		},
		{
			Name:        "reject_task",
			Description: "Reject a local task, moving it to the terminal 'rejected' state. Use this when the classifier's plan is wrong or the task should not be run at all. Rejected tasks are not retried.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "integer", "description": "Task ID to reject"},
				},
				"required": []string{"task_id"},
			},
		},
		{
			Name:        "clarify_task",
			Description: "Answer the clarifying questions on a task in 'pending_clarification' status and re-run the classify+plan pipeline with the clarified description. Pass the questions and answers as an array of objects. The task will transition back to pending/approved/pending_clarification based on the new classifier verdict.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "integer", "description": "Task ID in pending_clarification status"},
					"answers": map[string]interface{}{
						"type":        "array",
						"description": "Array of {question, answer} objects. Empty answers are dropped before re-running the pipeline.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"question": map[string]interface{}{"type": "string"},
								"answer":   map[string]interface{}{"type": "string"},
							},
						},
					},
				},
				"required": []string{"task_id", "answers"},
			},
		},
		{
			Name:        "delete_task",
			Description: "Hard-delete a task and its FTS row. Refuses to delete tasks in 'executing' status — stop them first via daemon shutdown or wait for completion. Irreversible; the task's branch (if any) is NOT deleted.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{"type": "integer", "description": "Task ID to delete"},
				},
				"required": []string{"task_id"},
			},
		},
		{
			Name:        "dedup_task",
			Description: "Check whether a proposed task description duplicates an existing task WITHOUT creating it. Runs all four cheap dedup layers (keyword, trigram, tf-idf, minhash) and optionally the LLM semantic escape hatch. Returns {is_duplicate, layer, score, task_id, reason}. Call this from the classifier BEFORE create_task or create_remote_task to avoid spawning work that duplicates something already in flight. Lightweight — typical cheap-layer run is <10ms.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"description": map[string]interface{}{"type": "string", "description": "Proposed task description to check"},
					"include_llm": map[string]interface{}{"type": "boolean", "description": "Also run LLM semantic dedup as the final layer (slower, ~5-20s)"},
				},
				"required": []string{"description"},
			},
		},
		{
			Name:        "create_remote_task",
			Description: "Create a task in a peer repo and BLOCK until the peer has classified and planned it. Returns the full task object including classification, files_affected, symbols_affected, plan (as a formatted string), and a next_action_hint telling you what to do next. The peer's daemon must be running. Typical next step: surface the plan to the user for review, then call approve_remote_task if the user agrees, then call wait_remote_task if your local work depends on the peer's result. Use classify_timeout_sec to control how long to wait for classification (default 120, max 600).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo":                 map[string]interface{}{"type": "string", "description": "Target repo name (from skep workspace list)"},
					"description":          map[string]interface{}{"type": "string", "description": "Task description"},
					"classify_timeout_sec": map[string]interface{}{"type": "integer", "description": "Max seconds to wait for peer classification (default 120, cap 600)"},
				},
				"required": []string{"repo", "description"},
			},
		},
		{
			Name:        "get_remote_task",
			Description: "Check the status of a task in another repo. Returns the full task object including result field. Use this to poll for completion after create_remote_task.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo":    map[string]interface{}{"type": "string", "description": "Target repo name"},
					"task_id": map[string]interface{}{"type": "integer", "description": "Task ID returned by create_remote_task"},
				},
				"required": []string{"repo", "task_id"},
			},
		},
		{
			Name:        "approve_remote_task",
			Description: "Approve a pending task in another repo so its daemon can execute it. Use this after create_remote_task if the task is stuck in 'pending' status (daemon only auto-executes small tasks when auto-execute-small is enabled).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo":    map[string]interface{}{"type": "string", "description": "Target repo name"},
					"task_id": map[string]interface{}{"type": "integer", "description": "Task ID to approve"},
				},
				"required": []string{"repo", "task_id"},
			},
		},
		{
			Name:        "wait_remote_task",
			Description: "Block until a remote task reaches a terminal state (done, failed, interrupted, rejected) or a timeout elapses. Use this when a local step genuinely depends on the remote result — the call returns the final task object with its result field populated. Prefer this over polling get_remote_task in a loop: you call it once and continue when the remote repo is finished. For fire-and-forget delegations where the caller doesn't need the result, skip this and continue local work in parallel.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo":        map[string]interface{}{"type": "string", "description": "Target repo name"},
					"task_id":     map[string]interface{}{"type": "integer", "description": "Task ID returned by create_remote_task"},
					"timeout_sec": map[string]interface{}{"type": "integer", "description": "Max seconds to wait (default 1800, cap 3600)"},
				},
				"required": []string{"repo", "task_id"},
			},
		},
	}

	return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{"tools": tools}}
}

func (s *Server) handleToolsCall(req request) response {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken interface{} `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResponse(req.ID, "invalid params")
	}
	progressToken := params.Meta.ProgressToken

	// Note: we do NOT refresh the index here. The index represents the
	// stable main branch state. Live changes the LLM makes on a task branch
	// are visible via the LLM's own file Read/Edit tools — not the index.
	// This keeps the index consistent across concurrent reads from other terminals.

	switch params.Name {
	case "get_overview":
		return s.toolGetOverview(req.ID)
	case "search_symbols":
		return s.toolSearchSymbols(req.ID, params.Arguments)
	case "get_call_graph":
		return s.toolGetCallGraph(req.ID, params.Arguments)
	case "get_file_context":
		return s.toolGetFileContext(req.ID, params.Arguments)
	case "list_tasks":
		return s.toolListTasks(req.ID)
	case "create_task":
		return s.toolCreateTask(req.ID, params.Arguments)
	case "show_task":
		return s.toolShowTask(req.ID, params.Arguments)
	case "approve_task":
		return s.toolApproveTask(req.ID, params.Arguments)
	case "reject_task":
		return s.toolRejectTask(req.ID, params.Arguments)
	case "clarify_task":
		return s.toolClarifyTask(req.ID, params.Arguments)
	case "delete_task":
		return s.toolDeleteTask(req.ID, params.Arguments)
	case "dedup_task":
		return s.toolDedupTask(req.ID, params.Arguments)
	case "create_remote_task":
		return s.toolCreateRemoteTask(req.ID, params.Arguments, progressToken)
	case "get_remote_task":
		return s.toolGetRemoteTask(req.ID, params.Arguments)
	case "approve_remote_task":
		return s.toolApproveRemoteTask(req.ID, params.Arguments)
	case "wait_remote_task":
		return s.toolWaitRemoteTask(req.ID, params.Arguments, progressToken)
	default:
		return errorResponse(req.ID, fmt.Sprintf("unknown tool: %s", params.Name))
	}
}

// --- Tool implementations ---

func (s *Server) toolGetOverview(id interface{}) response {
	fc, _ := s.store.FileCount()
	sc, _ := s.store.SymbolCount()
	topSyms, _ := s.store.TopSymbols(20)

	var symbols []map[string]interface{}
	for _, sym := range topSyms {
		symbols = append(symbols, map[string]interface{}{
			"id": sym.ID, "name": sym.Name, "kind": sym.Kind,
			"file": sym.FilePath, "line": sym.Line, "signature": sym.Signature,
		})
	}

	taskList, _ := tasks.List(s.store)
	var taskSummary []map[string]interface{}
	for _, t := range taskList {
		taskSummary = append(taskSummary, map[string]interface{}{
			"id": t.ID, "name": t.Name, "status": t.Status,
		})
	}

	// Peer repos in the same workspace (for cross-repo delegation via MCP)
	var peerRepos []string
	reg, _ := registry.Load()
	if reg != nil {
		repoName := filepath.Base(s.root)
		for name := range reg.Agents {
			if name != repoName {
				peerRepos = append(peerRepos, name)
			}
		}
	}

	overview := map[string]interface{}{
		"root":        s.root,
		"files":       fc,
		"symbols":     sc,
		"top_symbols": symbols,
		"tasks":       taskSummary,
		"peer_repos":  peerRepos,
	}

	data, _ := json.Marshal(overview)
	return textResponse(id, string(data))
}

func (s *Server) toolSearchSymbols(id interface{}, args json.RawMessage) response {
	var params struct {
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		Verbose bool   `json:"verbose"`
	}
	json.Unmarshal(args, &params)
	if params.Query == "" {
		return errorResponse(id, "query is required")
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}

	syms, err := s.store.SearchSymbols(params.Query, params.Limit)
	if err != nil {
		return errorResponse(id, err.Error())
	}

	// Default output drops doc_comment (often long, rarely load-bearing for planning).
	// Callers pass verbose=true when they explicitly need doc comments.
	var results []map[string]interface{}
	for _, sym := range syms {
		row := map[string]interface{}{
			"id": sym.ID, "name": sym.Name, "kind": sym.Kind,
			"file": sym.FilePath, "line": sym.Line, "end_line": sym.EndLine,
			"signature": truncSignature(sym.Signature),
		}
		if params.Verbose && sym.DocComment != "" {
			row["doc_comment"] = sym.DocComment
		}
		results = append(results, row)
	}

	data, _ := json.Marshal(results)
	return textResponse(id, string(data))
}

// truncSignature caps a signature string at 200 chars to avoid bloating
// prompt context with very long Go generic / TypeScript generic lines.
// Preserves the first 200 and adds a trailing marker.
func truncSignature(sig string) string {
	const cap = 200
	if len(sig) <= cap {
		return sig
	}
	return sig[:cap] + "…"
}

func (s *Server) toolGetCallGraph(id interface{}, args json.RawMessage) response {
	var params struct {
		SymbolID string `json:"symbol_id"`
	}
	json.Unmarshal(args, &params)
	if params.SymbolID == "" {
		return errorResponse(id, "symbol_id is required")
	}

	callers, _ := s.store.Callers(params.SymbolID)
	callees, _ := s.store.Callees(params.SymbolID)

	formatSyms := func(syms []*index.Symbol) []map[string]interface{} {
		var out []map[string]interface{}
		for _, sym := range syms {
			out = append(out, map[string]interface{}{
				"id": sym.ID, "name": sym.Name, "kind": sym.Kind,
				"file": sym.FilePath, "line": sym.Line, "signature": sym.Signature,
			})
		}
		return out
	}

	result := map[string]interface{}{
		"symbol_id": params.SymbolID,
		"callers":   formatSyms(callers),
		"callees":   formatSyms(callees),
	}

	data, _ := json.Marshal(result)
	return textResponse(id, string(data))
}

func (s *Server) toolGetFileContext(id interface{}, args json.RawMessage) response {
	var params struct {
		FilePath string `json:"file_path"`
		Verbose  bool   `json:"verbose"`
	}
	json.Unmarshal(args, &params)
	if params.FilePath == "" {
		return errorResponse(id, "file_path is required")
	}

	syms, err := s.store.SymbolsByFile(params.FilePath)
	if err != nil {
		return errorResponse(id, err.Error())
	}

	var results []map[string]interface{}
	for _, sym := range syms {
		row := map[string]interface{}{
			"id": sym.ID, "name": sym.Name, "kind": sym.Kind,
			"line": sym.Line, "end_line": sym.EndLine,
			"signature": truncSignature(sym.Signature),
		}
		if params.Verbose && sym.DocComment != "" {
			row["doc_comment"] = sym.DocComment
		}
		results = append(results, row)
	}

	data, _ := json.Marshal(results)
	return textResponse(id, string(data))
}

func (s *Server) toolListTasks(id interface{}) response {
	taskList, err := tasks.List(s.store)
	if err != nil {
		return errorResponse(id, err.Error())
	}

	data, _ := json.Marshal(taskList)
	return textResponse(id, string(data))
}

func (s *Server) toolCreateTask(id interface{}, args json.RawMessage) response {
	var params struct {
		Description string `json:"description"`
	}
	json.Unmarshal(args, &params)
	if params.Description == "" {
		return errorResponse(id, "description is required")
	}

	task, dedup, err := tasks.Create(s.store, params.Description, "", "")
	if err != nil {
		return errorResponse(id, err.Error())
	}
	if dedup != nil && dedup.IsDuplicate {
		result := map[string]interface{}{"duplicate": true, "reason": dedup.Reason, "task_id": dedup.TaskID}
		data, _ := json.Marshal(result)
		return textResponse(id, string(data))
	}

	result := map[string]interface{}{"task_id": task.ID, "name": task.Name, "status": task.Status}
	data, _ := json.Marshal(result)
	return textResponse(id, string(data))
}

func (s *Server) toolShowTask(id interface{}, args json.RawMessage) response {
	var params struct {
		TaskID int `json:"task_id"`
	}
	json.Unmarshal(args, &params)
	if params.TaskID <= 0 {
		return errorResponse(id, "task_id is required")
	}
	task, err := tasks.Get(s.store, params.TaskID)
	if err != nil {
		return errorResponse(id, err.Error())
	}
	out := map[string]interface{}{
		"task_id":        task.ID,
		"name":           task.Name,
		"description":    task.Description,
		"status":         task.Status,
		"classification": task.Classification,
		"plan":           task.Plan,
		"session_id":     task.SessionID,
		"branch":         task.Branch,
		"tokens_used":    task.TokensUsed,
		"result":         task.Result,
		"created_at":     task.CreatedAt,
	}
	if task.Status == tasks.StatusPendingClarification {
		if answers, rerr := tasks.ReadClarifyAnswers(s.skepDir, task.ID); rerr == nil {
			out["clarify_questions"] = answers
			out["clarify_file"] = tasks.ClarifyFilePath(s.skepDir, task.ID)
		}
	}
	data, _ := json.Marshal(out)
	return textResponse(id, string(data))
}

func (s *Server) toolApproveTask(id interface{}, args json.RawMessage) response {
	var params struct {
		TaskID int `json:"task_id"`
	}
	json.Unmarshal(args, &params)
	if params.TaskID <= 0 {
		return errorResponse(id, "task_id is required")
	}
	if err := tasks.Approve(s.store, params.TaskID); err != nil {
		return errorResponse(id, err.Error())
	}
	// Nudge the local daemon so it picks up the newly-approved task.
	if daemon.IsRunning(s.skepDir) {
		daemon.Send(s.skepDir, daemon.Request{Cmd: "notify_task", TaskID: params.TaskID})
	}
	data, _ := json.Marshal(map[string]interface{}{"task_id": params.TaskID, "status": tasks.StatusApproved})
	return textResponse(id, string(data))
}

func (s *Server) toolRejectTask(id interface{}, args json.RawMessage) response {
	var params struct {
		TaskID int `json:"task_id"`
	}
	json.Unmarshal(args, &params)
	if params.TaskID <= 0 {
		return errorResponse(id, "task_id is required")
	}
	if err := tasks.Reject(s.store, params.TaskID); err != nil {
		return errorResponse(id, err.Error())
	}
	data, _ := json.Marshal(map[string]interface{}{"task_id": params.TaskID, "status": tasks.StatusRejected})
	return textResponse(id, string(data))
}

func (s *Server) toolDeleteTask(id interface{}, args json.RawMessage) response {
	var params struct {
		TaskID int `json:"task_id"`
	}
	json.Unmarshal(args, &params)
	if params.TaskID <= 0 {
		return errorResponse(id, "task_id is required")
	}
	task, err := tasks.Get(s.store, params.TaskID)
	if err != nil {
		return errorResponse(id, err.Error())
	}
	if task.Status == tasks.StatusExecuting {
		return errorResponse(id, fmt.Sprintf("task #%d is executing; stop the daemon or wait for completion before deleting", params.TaskID))
	}
	if err := tasks.Delete(s.store, params.TaskID); err != nil {
		return errorResponse(id, err.Error())
	}
	data, _ := json.Marshal(map[string]interface{}{"task_id": params.TaskID, "deleted": true})
	return textResponse(id, string(data))
}

// toolClarifyTask writes the supplied answers to the clarify file and
// re-runs the classify+plan pipeline against the clarified description.
// Mirrors `skep task clarify <id>` but sources the answers from the MCP
// call instead of reading them from disk — so a classifier that already
// asked the user its own questions can finish the round-trip without
// touching the filesystem.
func (s *Server) toolClarifyTask(id interface{}, args json.RawMessage) response {
	var params struct {
		TaskID  int               `json:"task_id"`
		Answers []tasks.ClarifyQA `json:"answers"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResponse(id, "invalid arguments")
	}
	if params.TaskID <= 0 {
		return errorResponse(id, "task_id is required")
	}
	if len(params.Answers) == 0 {
		return errorResponse(id, "answers is required")
	}

	task, err := tasks.Get(s.store, params.TaskID)
	if err != nil {
		return errorResponse(id, err.Error())
	}
	if task.Status != tasks.StatusPendingClarification {
		return errorResponse(id, fmt.Sprintf("task #%d is %s, not pending_clarification", params.TaskID, task.Status))
	}

	// Drop empty answers — the CLI rejects them outright, and silently
	// dropping them here matches that spirit without punishing a well-
	// behaved classifier that sent optional fields.
	var answers []tasks.ClarifyQA
	for _, qa := range params.Answers {
		if qa.Answer != "" {
			answers = append(answers, qa)
		}
	}
	if len(answers) == 0 {
		return errorResponse(id, "no non-empty answers")
	}

	clarified := tasks.BuildClarifiedDescription(task.Description, answers)
	transient := *task
	transient.Description = clarified

	cfg := config.Load(s.skepDir)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	result, perr := tasks.ClassifyAndPlanCtx(ctx, s.store, s.root, cfg.ClassifyCmd(), cfg.PlanCmd(), &transient)
	if perr != nil {
		return errorResponse(id, fmt.Sprintf("re-classify: %v", perr))
	}
	if result == nil || result.Classification == "" {
		return errorResponse(id, "re-classify returned no result")
	}

	task.Classification = result.Classification
	task.Plan = tasks.FormatPipelineResult(result)
	if len(result.Plan) > 0 {
		if raw, mErr := json.Marshal(result.Plan); mErr == nil {
			task.PlanStepsJSON = string(raw)
		}
	}
	switch {
	case result.Classification == "reject":
		task.Status = tasks.StatusRejected
		task.Result = result.RejectReason
	case result.NeedsClarification:
		task.Status = tasks.StatusPendingClarification
		if path, werr := tasks.WriteClarifyFile(s.skepDir, task.ID, task.Description, result.ClarifyingQuestions); werr == nil {
			task.Result = "still needs clarification: " + path
		}
	case result.Classification == "small" && cfg.AutoExecuteSmall:
		task.Status = tasks.StatusApproved
		task.Result = ""
	default:
		task.Status = tasks.StatusPending
		task.Result = ""
	}
	if err := tasks.Update(s.store, task); err != nil {
		return errorResponse(id, fmt.Sprintf("update task: %v", err))
	}

	if daemon.IsRunning(s.skepDir) {
		daemon.Send(s.skepDir, daemon.Request{Cmd: "notify_task", TaskID: task.ID})
	}

	out := map[string]interface{}{
		"task_id":        task.ID,
		"status":         task.Status,
		"classification": task.Classification,
		"plan":           task.Plan,
	}
	if task.Status == tasks.StatusPendingClarification {
		out["clarify_questions"] = result.ClarifyingQuestions
	}
	data, _ := json.Marshal(out)
	return textResponse(id, string(data))
}

// toolDedupTask runs the dedup stack without creating a task. Used by
// classifiers (and any other MCP client) as a preflight check: "would
// this task be a duplicate if I created it right now?" Answers are
// advisory — the client decides whether to proceed, rephrase, or bail.
func (s *Server) toolDedupTask(id interface{}, args json.RawMessage) response {
	var params struct {
		Description string `json:"description"`
		IncludeLLM  bool   `json:"include_llm"`
	}
	json.Unmarshal(args, &params)
	if params.Description == "" {
		return errorResponse(id, "description is required")
	}

	// Run the four cheap layers in the same order as cmdTaskCreate.
	// Short-circuit on the first hit; each layer already logs its own
	// outcome to .skep/log/dedup.log.
	layers := []func() (*tasks.DedupResult, error){
		func() (*tasks.DedupResult, error) {
			return tasks.CheckDedupInRoot(s.store, params.Description, s.root)
		},
		func() (*tasks.DedupResult, error) {
			return tasks.CheckDedupTrigram(s.store, params.Description, s.root)
		},
		func() (*tasks.DedupResult, error) {
			return tasks.CheckDedupTFIDF(s.store, params.Description, s.root)
		},
		func() (*tasks.DedupResult, error) {
			return tasks.CheckDedupMinHash(s.store, params.Description, s.root)
		},
	}
	for _, layer := range layers {
		r, _ := layer()
		if r != nil && r.IsDuplicate {
			data, _ := json.Marshal(r)
			return textResponse(id, string(data))
		}
	}

	// Optional LLM escape hatch. Uses the DedupCmd (Haiku by default)
	// so that a classifier calling this tool doesn't accidentally burn
	// Opus tokens on a "are these two strings about the same thing?"
	// question.
	if params.IncludeLLM {
		cfg := config.Load(filepath.Join(s.root, ".skep"))
		llmDup, _ := tasks.CheckDedupLLM(s.store, params.Description, s.root, cfg.DedupCmd())
		if llmDup != nil && llmDup.IsDuplicate {
			data, _ := json.Marshal(llmDup)
			return textResponse(id, string(data))
		}
	}

	data, _ := json.Marshal(&tasks.DedupResult{IsDuplicate: false})
	return textResponse(id, string(data))
}

func (s *Server) toolCreateRemoteTask(id interface{}, args json.RawMessage, progressToken interface{}) response {
	var params struct {
		Repo               string `json:"repo"`
		Description        string `json:"description"`
		ClassifyTimeoutSec int    `json:"classify_timeout_sec"`
	}
	json.Unmarshal(args, &params)
	if params.Repo == "" || params.Description == "" {
		return errorResponse(id, "repo and description are required")
	}

	classifyTimeout := params.ClassifyTimeoutSec
	if classifyTimeout <= 0 {
		classifyTimeout = 120 // default 2 min
	}
	if classifyTimeout > 600 {
		classifyTimeout = 600 // cap 10 min
	}

	reg, err := registry.Load()
	if err != nil {
		return errorResponse(id, "failed to load registry")
	}

	agent := reg.Get(params.Repo)
	if agent == nil {
		return errorResponse(id, fmt.Sprintf("repo '%s' not found in registry", params.Repo))
	}

	depSkepDir := filepath.Join(agent.Path, ".skep")
	sourceRepo := filepath.Base(s.root)

	var taskID int
	var duplicate bool
	var dupReason string

	// Try daemon first
	if resp, err := daemon.Send(depSkepDir, daemon.Request{
		Cmd:         "create_task",
		Description: params.Description,
		SourceRepo:  sourceRepo,
	}); err == nil && resp.OK {
		if data, ok := resp.Data.(map[string]interface{}); ok {
			if dup, _ := data["duplicate"].(bool); dup {
				duplicate = true
				if r, _ := data["reason"].(string); r != "" {
					dupReason = r
				}
				if tid, ok := data["task_id"].(float64); ok {
					taskID = int(tid)
				}
			} else if tid, ok := data["task_id"].(float64); ok {
				taskID = int(tid)
			}
		}
	}

	// Fallback: write directly to the target repo's index.db
	if taskID == 0 && !duplicate {
		depDBPath := filepath.Join(depSkepDir, "index.db")
		depStore, openErr := index.OpenStore(depDBPath)
		if openErr != nil {
			return errorResponse(id, fmt.Sprintf("cannot reach %s (no daemon, no index: %v)", params.Repo, openErr))
		}
		task, dedup, createErr := tasks.Create(depStore, params.Description, sourceRepo, "")
		depStore.Close()
		if createErr != nil {
			return errorResponse(id, fmt.Sprintf("create task in %s: %v", params.Repo, createErr))
		}
		if dedup != nil && dedup.IsDuplicate {
			duplicate = true
			dupReason = dedup.Reason
			taskID = dedup.TaskID
		} else if task != nil {
			taskID = task.ID
		}
	}

	if duplicate {
		result := map[string]interface{}{
			"duplicate": true,
			"reason":    dupReason,
			"task_id":   taskID,
			"repo":      params.Repo,
			"note":      "an equivalent task already exists; fetch it with get_remote_task to review its plan",
		}
		data, _ := json.Marshal(result)
		return textResponse(id, string(data))
	}

	if taskID == 0 {
		return errorResponse(id, fmt.Sprintf("create_remote_task on %s returned no task id", params.Repo))
	}

	// Block until the peer has classified + planned the task so the caller
	// can review the plan before approving. The peer daemon's classifier
	// runs inline (typically 5-30s); we wait up to classifyTimeout.
	depDBPath := filepath.Join(depSkepDir, "index.db")
	depStore, err := index.OpenStore(depDBPath)
	if err != nil {
		// Can't poll for the plan — return what we know.
		result := map[string]interface{}{
			"task_id": taskID,
			"status":  tasks.StatusCreated,
			"repo":    params.Repo,
			"note":    fmt.Sprintf("task created but cannot poll for plan: %v", err),
		}
		data, _ := json.Marshal(result)
		return textResponse(id, string(data))
	}
	defer depStore.Close()

	start := time.Now()
	deadline := start.Add(time.Duration(classifyTimeout) * time.Second)
	const pollInterval = 1 * time.Second
	const progressEvery = 5 * time.Second
	lastProgress := time.Now()

	notifyProgress(progressToken, 0, float64(classifyTimeout),
		fmt.Sprintf("created task #%d in %s, waiting for classification", taskID, params.Repo))

	for {
		task, gerr := tasks.Get(depStore, taskID)
		if gerr != nil {
			return errorResponse(id, fmt.Sprintf("poll peer task #%d in %s: %v", taskID, params.Repo, gerr))
		}
		// Classification done when status has advanced past 'created'.
		if task.Status != tasks.StatusCreated {
			notifyProgress(progressToken, float64(classifyTimeout), float64(classifyTimeout),
				fmt.Sprintf("peer classified as %s", task.Classification))
			result := map[string]interface{}{
				"task_id":          task.ID,
				"name":             task.Name,
				"status":           task.Status,
				"classification":   task.Classification,
				"plan":             task.Plan,
				"repo":             params.Repo,
				"next_action_hint": nextActionHint(task.Status),
			}
			// If the peer classifier flagged the task as ambiguous,
			// surface the question list so the calling agent can
			// present them to the user instead of waiting silently.
			if task.Status == tasks.StatusPendingClarification {
				if answers, rerr := tasks.ReadClarifyAnswers(depSkepDir, task.ID); rerr == nil {
					questions := make([]string, 0, len(answers))
					for _, qa := range answers {
						questions = append(questions, qa.Question)
					}
					result["clarification_questions"] = questions
					result["next_action_hint"] = fmt.Sprintf("Peer classifier says this task is ambiguous. Surface the questions to the user; once answered, write the answers into %s (one per `A:` line) and run `skep task clarify %d` in %s to re-classify.", tasks.ClarifyFilePath(depSkepDir, task.ID), task.ID, params.Repo)
				}
			}
			data, _ := json.Marshal(result)
			return textResponse(id, string(data))
		}
		if time.Now().After(deadline) {
			result := map[string]interface{}{
				"task_id": taskID,
				"status":  tasks.StatusCreated,
				"repo":    params.Repo,
				"note":    fmt.Sprintf("peer has not classified within %ds; call get_remote_task later to fetch the plan", classifyTimeout),
			}
			data, _ := json.Marshal(result)
			return textResponse(id, string(data))
		}
		if time.Since(lastProgress) >= progressEvery {
			elapsed := time.Since(start).Seconds()
			notifyProgress(progressToken, elapsed, float64(classifyTimeout),
				fmt.Sprintf("still waiting for peer classification (%.0fs elapsed)", elapsed))
			lastProgress = time.Now()
		}
		time.Sleep(pollInterval)
	}
}

// nextActionHint tells the sender LLM what to do next based on the peer's
// current status after classification.
func nextActionHint(status string) string {
	switch status {
	case tasks.StatusPending:
		return "Review the plan above. If it looks right, call approve_remote_task(repo, task_id) to let the peer daemon execute it. Otherwise surface the plan to the user and wait for guidance."
	case tasks.StatusApproved:
		return "Peer has already auto-approved this task (auto-execute-small). Call wait_remote_task(repo, task_id) if you need to block on completion."
	case tasks.StatusExecuting:
		return "Peer is already executing this task. Call wait_remote_task(repo, task_id) to block until it finishes."
	case tasks.StatusDone:
		return "Peer has already finished this task (likely a very fast or trivial one). Read the plan and result before proceeding."
	case tasks.StatusFailed, tasks.StatusInterrupted, tasks.StatusRejected:
		return fmt.Sprintf("Peer task ended in '%s'. Surface the reason to the user — do not retry automatically.", status)
	case tasks.StatusPendingClarification:
		return "Peer classifier flagged this task as ambiguous. See clarification_questions; surface them to the user and record answers via skep task clarify on the peer."
	}
	return "Fetch with get_remote_task to see the latest status."
}

func (s *Server) toolGetRemoteTask(id interface{}, args json.RawMessage) response {
	var params struct {
		Repo   string `json:"repo"`
		TaskID int    `json:"task_id"`
	}
	json.Unmarshal(args, &params)
	if params.Repo == "" || params.TaskID == 0 {
		return errorResponse(id, "repo and task_id are required")
	}

	reg, err := registry.Load()
	if err != nil {
		return errorResponse(id, "failed to load registry")
	}
	agent := reg.Get(params.Repo)
	if agent == nil {
		return errorResponse(id, fmt.Sprintf("repo '%s' not found in registry", params.Repo))
	}

	depSkepDir := filepath.Join(agent.Path, ".skep")

	// Try daemon first
	if resp, err := daemon.Send(depSkepDir, daemon.Request{Cmd: "get_task", TaskID: params.TaskID}); err == nil && resp.OK {
		data, _ := json.Marshal(resp.Data)
		return textResponse(id, string(data))
	}

	// Fallback: read directly from the target repo's index.db
	depDBPath := filepath.Join(depSkepDir, "index.db")
	depStore, err := index.OpenStore(depDBPath)
	if err != nil {
		return errorResponse(id, fmt.Sprintf("cannot reach %s: %v", params.Repo, err))
	}
	defer depStore.Close()

	task, err := tasks.Get(depStore, params.TaskID)
	if err != nil {
		return errorResponse(id, fmt.Sprintf("task #%d not found in %s", params.TaskID, params.Repo))
	}

	// For tasks stuck in pending_clarification, surface the question
	// list so the caller can present them to the user without having
	// to read the clarify file manually.
	out := map[string]interface{}{
		"task": task,
	}
	if task.Status == tasks.StatusPendingClarification {
		if answers, rerr := tasks.ReadClarifyAnswers(depSkepDir, task.ID); rerr == nil {
			questions := make([]string, 0, len(answers))
			for _, qa := range answers {
				questions = append(questions, qa.Question)
			}
			out["clarification_questions"] = questions
			out["next_action_hint"] = fmt.Sprintf("Task is waiting for clarification. Surface these questions to the user; their answers must be recorded in %s (one per `A:` line), then run `skep task clarify %d` in %s to re-classify.", tasks.ClarifyFilePath(depSkepDir, task.ID), task.ID, params.Repo)
		}
	}
	data, _ := json.Marshal(out)
	return textResponse(id, string(data))
}

func (s *Server) toolApproveRemoteTask(id interface{}, args json.RawMessage) response {
	var params struct {
		Repo   string `json:"repo"`
		TaskID int    `json:"task_id"`
	}
	json.Unmarshal(args, &params)
	if params.Repo == "" || params.TaskID == 0 {
		return errorResponse(id, "repo and task_id are required")
	}

	reg, err := registry.Load()
	if err != nil {
		return errorResponse(id, "failed to load registry")
	}
	agent := reg.Get(params.Repo)
	if agent == nil {
		return errorResponse(id, fmt.Sprintf("repo '%s' not found in registry", params.Repo))
	}

	depSkepDir := filepath.Join(agent.Path, ".skep")

	// Try daemon first — it'll pick up the approved task immediately
	if resp, err := daemon.Send(depSkepDir, daemon.Request{Cmd: "approve_task", TaskID: params.TaskID}); err == nil && resp.OK {
		data, _ := json.Marshal(resp.Data)
		return textResponse(id, string(data))
	}

	// Fallback: approve directly via DB
	depDBPath := filepath.Join(depSkepDir, "index.db")
	depStore, err := index.OpenStore(depDBPath)
	if err != nil {
		return errorResponse(id, fmt.Sprintf("cannot reach %s: %v", params.Repo, err))
	}
	defer depStore.Close()

	if err := tasks.Approve(depStore, params.TaskID); err != nil {
		return errorResponse(id, fmt.Sprintf("approve task #%d in %s: %v", params.TaskID, params.Repo, err))
	}

	result := map[string]interface{}{
		"task_id": params.TaskID,
		"status":  tasks.StatusApproved,
		"repo":    params.Repo,
		"note":    "daemon not running in target repo — approved directly via DB (will execute when daemon starts)",
	}
	data, _ := json.Marshal(result)
	return textResponse(id, string(data))
}

// toolWaitRemoteTask blocks until the remote task reaches a terminal state
// (done, failed, interrupted, rejected) or the timeout elapses. Used when a
// local step genuinely depends on the remote result, so the caller doesn't
// have to implement a polling loop.
//
// Emits MCP progress notifications every 5 seconds so the client can show
// the user live status ("peer is executing, 40s elapsed") instead of just
// blocking silently.
func (s *Server) toolWaitRemoteTask(id interface{}, args json.RawMessage, progressToken interface{}) response {
	var params struct {
		Repo       string `json:"repo"`
		TaskID     int    `json:"task_id"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	json.Unmarshal(args, &params)
	if params.Repo == "" || params.TaskID == 0 {
		return errorResponse(id, "repo and task_id are required")
	}

	timeout := params.TimeoutSec
	if timeout <= 0 {
		timeout = 1800 // 30 min default
	}
	if timeout > 3600 {
		timeout = 3600 // 1 hr cap
	}

	reg, err := registry.Load()
	if err != nil {
		return errorResponse(id, "failed to load registry")
	}
	agent := reg.Get(params.Repo)
	if agent == nil {
		return errorResponse(id, fmt.Sprintf("repo '%s' not found in registry", params.Repo))
	}

	depDBPath := filepath.Join(agent.Path, ".skep", "index.db")
	depStore, err := index.OpenStore(depDBPath)
	if err != nil {
		return errorResponse(id, fmt.Sprintf("cannot reach %s: %v", params.Repo, err))
	}
	defer depStore.Close()

	start := time.Now()
	deadline := start.Add(time.Duration(timeout) * time.Second)
	const pollInterval = 2 * time.Second
	const progressEvery = 5 * time.Second
	lastProgress := time.Now()
	lastStatusReported := ""

	notifyProgress(progressToken, 0, float64(timeout),
		fmt.Sprintf("waiting on %s task #%d", params.Repo, params.TaskID))

	for {
		task, err := tasks.Get(depStore, params.TaskID)
		if err != nil {
			return errorResponse(id, fmt.Sprintf("task #%d not found in %s: %v", params.TaskID, params.Repo, err))
		}

		// Terminal states — return the final task object with a clear outcome summary.
		switch task.Status {
		case tasks.StatusDone:
			notifyProgress(progressToken, float64(timeout), float64(timeout),
				fmt.Sprintf("%s task #%d done", params.Repo, params.TaskID))
			result := map[string]interface{}{
				"task":             task,
				"outcome":          tasks.StatusDone,
				"next_action_hint": "Read task.result (the peer's summary) and the committed branch if you need to reference what the peer produced. Proceed with your local steps.",
			}
			data, _ := json.Marshal(result)
			return textResponse(id, string(data))
		case tasks.StatusFailed:
			notifyProgress(progressToken, float64(timeout), float64(timeout),
				fmt.Sprintf("%s task #%d failed", params.Repo, params.TaskID))
			result := map[string]interface{}{
				"task":             task,
				"outcome":          tasks.StatusFailed,
				"error_summary":    task.Result,
				"next_action_hint": "Surface the failure reason above to the user. Do NOT retry automatically. Do NOT proceed with dependent local steps.",
			}
			data, _ := json.Marshal(result)
			return textResponse(id, string(data))
		case tasks.StatusRejected:
			result := map[string]interface{}{
				"task":             task,
				"outcome":          tasks.StatusRejected,
				"next_action_hint": "A human rejected this task on the peer side. Surface that to the user and ask what to do.",
			}
			data, _ := json.Marshal(result)
			return textResponse(id, string(data))
		case tasks.StatusInterrupted:
			result := map[string]interface{}{
				"task":             task,
				"outcome":          tasks.StatusInterrupted,
				"note":             "peer has commits on its task branch but they are not in the base branch yet — a human needs to merge, or the session exited mid-run",
				"next_action_hint": "Do NOT assume the peer is done. Surface task.branch and task.result to the user so they can review the commits and decide whether to merge.",
			}
			data, _ := json.Marshal(result)
			return textResponse(id, string(data))
		}

		if time.Now().After(deadline) {
			return errorResponse(id, fmt.Sprintf(
				"timeout after %ds waiting for task #%d in %s (last status: %s) — use get_remote_task to check later, or re-call wait_remote_task with a longer timeout_sec",
				timeout, params.TaskID, params.Repo, task.Status))
		}

		// Periodic progress notification. Also emit immediately on status change
		// so the client sees "classified → approved → executing" transitions.
		if task.Status != lastStatusReported || time.Since(lastProgress) >= progressEvery {
			elapsed := time.Since(start).Seconds()
			notifyProgress(progressToken, elapsed, float64(timeout),
				fmt.Sprintf("%s task #%d: %s (%.0fs elapsed)", params.Repo, params.TaskID, task.Status, elapsed))
			lastStatusReported = task.Status
			lastProgress = time.Now()
		}
		time.Sleep(pollInterval)
	}
}

// --- Helpers ---

func textResponse(id interface{}, text string) response {
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Result: toolResult{
			Content: []contentBlock{{Type: "text", Text: text}},
		},
	}
}

func errorResponse(id interface{}, msg string) response {
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Result: toolResult{
			Content: []contentBlock{{Type: "text", Text: "Error: " + msg}},
			IsError: true,
		},
	}
}

func writeResponse(resp response) {
	data, _ := json.Marshal(resp)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Fprintf(os.Stdout, "%s\n", data)
}

// progressNotification is an MCP notifications/progress message sent mid-tool
// so the client can show the user what a long-running tool is doing.
type progressNotification struct {
	JSONRPC string               `json:"jsonrpc"`
	Method  string               `json:"method"`
	Params  progressNotifyParams `json:"params"`
}

type progressNotifyParams struct {
	ProgressToken interface{} `json:"progressToken"`
	Progress      float64     `json:"progress"`
	Total         float64     `json:"total,omitempty"`
	Message       string      `json:"message,omitempty"`
}

// notifyProgress emits an MCP progress notification on stdout. The client
// matches it to the in-flight tool call via progressToken. If progressToken
// is nil, the client did not request progress — silently skip.
func notifyProgress(token interface{}, progress, total float64, message string) {
	if token == nil {
		return
	}
	msg := progressNotification{
		JSONRPC: "2.0",
		Method:  "notifications/progress",
		Params: progressNotifyParams{
			ProgressToken: token,
			Progress:      progress,
			Total:         total,
			Message:       message,
		},
	}
	data, _ := json.Marshal(msg)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Fprintf(os.Stdout, "%s\n", data)
}
