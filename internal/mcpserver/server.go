// Package mcpserver exposes the local task board as narrow Model Context
// Protocol tools for desktop chat clients.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kcompton15/Todo-Board/internal/store"
)

const (
	serverName            = "todo-board"
	serverVersion         = "1.0.0"
	latestProtocolVersion = "2025-11-25"
	maxRPCMessage         = 1 << 20
	mcpInstructions       = "Use list_tasks before create_task to avoid duplicates. A plain answer creates no card. Create work when the human asks for a change; create review when the human asks for a review and put the merge request URL in notes; create followup only when the conversation produces something the human must do; create probe only when a question required real investigation and left artifacts worth finding again. Treat one task as one human-sized outcome and use subtasks for steps. Close your own review and probe cards when the deliverable is returned. Close work only when the human asked for that work and it is verified; otherwise leave it open and record a next_step saying it is ready to close through a work-log-capable client. Never move a followup card. These lifecycle updates are authorized side effects; do not ask separately whether to perform them."
)

var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

// Server translates MCP JSON-RPC calls into board API requests.
type Server struct {
	board *BoardClient
}

// New returns a task-board MCP server.
func New(board *BoardClient) *Server {
	return &Server{board: board}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

// Serve reads newline-delimited JSON-RPC requests and writes responses. MCP
// notifications have no ID and intentionally produce no response.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if s == nil || s.board == nil {
		return errors.New("MCP server requires a board client")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maxRPCMessage)
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := s.handle(ctx, line, encoder); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP request: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, payload []byte, encoder *json.Encoder) error {
	var request rpcRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return writeRPCError(encoder, nil, -32700, "Parse error")
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		if len(request.ID) == 0 {
			return nil
		}
		return writeRPCError(encoder, request.ID, -32600, "Invalid Request")
	}
	if len(request.ID) == 0 {
		return nil
	}

	switch request.Method {
	case "initialize":
		return s.initialize(request, encoder)
	case "ping":
		return writeRPCResult(encoder, request.ID, map[string]any{})
	case "tools/list":
		return writeRPCResult(encoder, request.ID, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		return s.callTool(ctx, request, encoder)
	default:
		return writeRPCError(encoder, request.ID, -32601, "Method not found")
	}
}

func (s *Server) initialize(request rpcRequest, encoder *json.Encoder) error {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || params.ProtocolVersion == "" {
		return writeRPCError(encoder, request.ID, -32602, "initialize requires protocolVersion")
	}
	protocolVersion := latestProtocolVersion
	if supportedProtocolVersions[params.ProtocolVersion] {
		protocolVersion = params.ProtocolVersion
	}
	result := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]string{
			"name": serverName, "title": "Todo Board", "version": serverVersion,
		},
		"instructions": mcpInstructions + " " + linkInstructions,
	}
	return writeRPCResult(encoder, request.ID, result)
}

func (s *Server) callTool(ctx context.Context, request rpcRequest, encoder *json.Encoder) error {
	var params callToolParams
	if err := decodeStrict(request.Params, &params); err != nil || params.Name == "" {
		return writeRPCError(encoder, request.ID, -32602, "tools/call requires a valid name and arguments")
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	}
	if err := store.ValidateNoNullFields(params.Arguments); err != nil {
		return writeRPCResult(encoder, request.ID, toolFailure(err))
	}

	result, err := s.dispatchTool(ctx, params.Name, params.Arguments)
	if err != nil {
		return writeRPCResult(encoder, request.ID, toolFailure(err))
	}
	return writeRPCResult(encoder, request.ID, toolSuccess(result))
}

func (s *Server) dispatchTool(ctx context.Context, name string, arguments json.RawMessage) (map[string]any, error) {
	switch name {
	case "list_tasks":
		var input ListTasksInput
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		if input.Lane != "" && !store.IsLane(input.Lane) {
			return nil, fmt.Errorf("invalid lane %q", input.Lane)
		}
		if input.Kind != "" && !store.IsKind(input.Kind) {
			return nil, fmt.Errorf("invalid kind %q", input.Kind)
		}
		if input.Priority < 0 || input.Priority > 3 {
			return nil, errors.New("priority must be 1, 2, or 3")
		}
		tasks, err := s.board.ListTasks(ctx, input)
		return map[string]any{"tasks": tasks}, err

	case "get_activity":
		var input GetTaskInput
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		return s.board.GetActivity(ctx, input)

	case "get_task":
		var input GetTaskInput
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		task, err := s.board.GetTask(ctx, input)
		return map[string]any{"task": task}, err

	case "create_task":
		var input CreateTaskInput
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		if strings.TrimSpace(input.Title) == "" {
			return nil, errors.New("title is required")
		}
		if input.Lane != "" && !store.IsLane(input.Lane) {
			return nil, fmt.Errorf("invalid lane %q", input.Lane)
		}
		if input.Kind != "" && !store.IsKind(input.Kind) {
			return nil, fmt.Errorf("invalid kind %q", input.Kind)
		}
		if input.Priority < 0 || input.Priority > 3 {
			return nil, errors.New("priority must be 1, 2, or 3")
		}
		task, err := s.board.CreateTask(ctx, input)
		return map[string]any{"task": task}, err

	case "update_task":
		var input struct {
			Links            *[]store.LinkInput `json:"links,omitempty"`
			AgentContext     *string            `json:"agentContext,omitempty"`
			ReconcileMode    *string            `json:"reconcileMode,omitempty"`
			ID               string             `json:"id"`
			ExpectedRevision *uint64            `json:"expectedRevision,omitempty"`
			Title            *string            `json:"title,omitempty"`
			Lane             *string            `json:"lane,omitempty"`
			Kind             *string            `json:"kind,omitempty"`
			Priority         *int               `json:"priority,omitempty"`
			Project          *string            `json:"project,omitempty"`
			Tag              *string            `json:"tag,omitempty"`
			Notes            *string            `json:"notes,omitempty"`
		}
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		if input.Lane != nil && !store.IsLane(*input.Lane) {
			return nil, fmt.Errorf("invalid lane %q", *input.Lane)
		}
		if input.Kind != nil && *input.Kind != "" && !store.IsKind(*input.Kind) {
			return nil, fmt.Errorf("invalid kind %q", *input.Kind)
		}
		if input.Priority != nil && (*input.Priority < 1 || *input.Priority > 3) {
			return nil, errors.New("priority must be 1, 2, or 3")
		}
		update := UpdateTaskInput{
			Links: input.Links, AgentContext: input.AgentContext, ReconcileMode: input.ReconcileMode,
			ID: input.ID, ExpectedRevision: input.ExpectedRevision, Title: input.Title, Lane: input.Lane, Kind: input.Kind, Priority: input.Priority,
			Project: input.Project, Tag: input.Tag, Notes: input.Notes,
		}
		task, err := s.board.UpdateTask(ctx, update)
		return map[string]any{"task": task}, err

	case "link_task":
		var input LinkTaskInput
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		link, err := s.board.LinkTask(ctx, input)
		return map[string]any{"link": link}, err
	case "unlink_task":
		var input UnlinkTaskInput
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		return s.board.UnlinkTask(ctx, input)
	case "log_review_delivered":
		var input ReviewDeliveredInput
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		entry, err := s.board.LogReviewDelivered(ctx, input)
		return map[string]any{"receipt": entry}, err
	case "add_subtask":
		var input AddSubtaskInput
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		if strings.TrimSpace(input.Title) == "" {
			return nil, errors.New("title is required")
		}
		if input.Lane != "" && !store.IsLane(input.Lane) {
			return nil, fmt.Errorf("invalid lane %q", input.Lane)
		}
		subtask, err := s.board.AddSubtask(ctx, input)
		return map[string]any{"subtask": subtask}, err

	case "update_subtask":
		var input struct {
			ID               string  `json:"id"`
			ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
			SubtaskID        string  `json:"subtaskId"`
			Title            *string `json:"title,omitempty"`
			Lane             *string `json:"lane,omitempty"`
			Done             *bool   `json:"done,omitempty"`
		}
		if err := decodeStrict(arguments, &input); err != nil {
			return nil, invalidArguments(err)
		}
		if input.Title != nil && strings.TrimSpace(*input.Title) == "" {
			return nil, errors.New("title cannot be empty")
		}
		if input.Lane != nil && !store.IsLane(*input.Lane) {
			return nil, fmt.Errorf("invalid lane %q", *input.Lane)
		}
		update := UpdateSubtaskInput{
			ID: input.ID, ExpectedRevision: input.ExpectedRevision, SubtaskID: input.SubtaskID, Title: input.Title,
			Lane: input.Lane, Done: input.Done,
		}
		subtask, err := s.board.UpdateSubtask(ctx, update)
		return map[string]any{"subtask": subtask}, err

	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func invalidArguments(err error) error {
	return fmt.Errorf("invalid arguments: %w", err)
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func toolSuccess(value map[string]any) map[string]any {
	return map[string]any{
		"content":           []map[string]string{{"type": "text", "text": mustJSON(value)}},
		"structuredContent": value,
		"isError":           false,
	}
}

func toolFailure(err error) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": "Todo Board: " + err.Error()}},
		"isError": true,
	}
}

func mustJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return `{"error":"could not encode tool result"}`
	}
	return string(payload)
}

func writeRPCResult(encoder *json.Encoder, id json.RawMessage, result any) error {
	return encoder.Encode(rpcResponse{JSONRPC: "2.0", ID: normalizeID(id), Result: result})
}

func writeRPCError(encoder *json.Encoder, id json.RawMessage, code int, message string) error {
	return encoder.Encode(rpcResponse{
		JSONRPC: "2.0", ID: normalizeID(id), Error: &rpcError{Code: code, Message: message},
	})
}

func normalizeID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func toolDefinitions() []toolDefinition {
	definitions := []toolDefinition{
		{Name: "get_activity", Title: "Read workboard activity", Description: "Read the latest 50 retained task changes with self-reported client attribution. Omit id for board activity, including deleted tasks. Use a full task ID to filter. History before the upgrade is unavailable.", InputSchema: objectSchema(map[string]any{"id": stringProperty("Optional full task ID")}, nil), Annotations: annotations(true, false, true)},
		{
			Name: "list_tasks", Title: "Find workboard tasks",
			Description: "Search the Todo Board before create_task to avoid duplicate cards. A plain answer creates no card. Filter by kind: work for a requested change, review for a requested review, followup for a human action, or probe for an investigated question with reusable artifacts. Filters are combined; with no filters, every task is returned.",
			InputSchema: objectSchema(map[string]any{
				"query":    stringProperty("Text to search in title, notes, and project"),
				"lane":     enumProperty("Lane filter", store.LaneOrder),
				"kind":     enumProperty("Card kind filter", store.KindOrder),
				"project":  stringProperty("Exact project or epic name"),
				"tag":      stringProperty("Exact work-type tag"),
				"priority": integerProperty("Priority filter: 1 critical, 2 normal, 3 low", 1, 3),
			}, nil),
			Annotations: annotations(true, false, true),
		},
		{
			Name: "get_task", Title: "Get one workboard task",
			Description: "Read one task and all of its subtasks using its full task ID.",
			InputSchema: objectSchema(map[string]any{
				"id": stringProperty("Full task ID"),
			}, []string{"id"}),
			Annotations: annotations(true, false, true),
		},
		{
			Name: "create_task", Title: "Create a workboard task",
			Description: "Create one human-sized outcome only after list_tasks finds no active match. Use kind work only for a human-requested change; review for a human-requested review and put the merge request URL in notes; followup only for something the human must do; probe only for a question that required real investigation and left reusable artifacts. A plain answer creates no card. Creation under these rules is an authorized side effect; do not ask separately. Use subtasks for concrete steps.",
			InputSchema: objectSchema(map[string]any{
				"title":    stringProperty("Required outcome-oriented task title"),
				"lane":     enumProperty("Initial lane; defaults to backlog", store.LaneOrder),
				"kind":     enumProperty("Card kind; defaults to work", store.KindOrder),
				"priority": integerProperty("1 critical, 2 normal (default), or 3 low", 1, 3),
				"project":  stringProperty("Project or epic grouping, such as Web or API"),
				"tag":      stringProperty("Work-type tag without spaces, such as Review or Incident"),
				"notes":    stringProperty("Context, decisions, links, or blocker details"),
				"subtasks": map[string]any{
					"type": "array", "description": "Concrete steps within this outcome",
					"items": map[string]any{"type": "string"}, "maxItems": store.MaxSubtasks,
				},
			}, []string{"title"}),
			Annotations: annotations(false, false, false),
		},
		{
			Name: "update_task", Title: "Update a workboard task",
			Description: "Change task fields, kind, or lane. Close the session's own review and probe cards when their deliverable is returned. Close work only when the human requested that work and it is verified; otherwise leave it open and record a next_step saying it is ready to close through a work-log-capable client. Never move followup. These lifecycle updates are authorized side effects; do not ask separately. Use notes for blocker reasons.",
			InputSchema: objectSchema(map[string]any{
				"id":               stringProperty("Full task ID"),
				"expectedRevision": map[string]any{"type": "integer", "minimum": 1, "description": "Revision from get_task. Use it to prevent overwriting another session; on conflict read again and reconcile."},
				"title":            stringProperty("Replacement title"),
				"lane":             enumProperty("New lane", store.LaneOrder),
				"kind":             enumProperty("New card kind", store.KindOrder),
				"priority":         integerProperty("1 critical, 2 normal, or 3 low", 1, 3),
				"project":          stringProperty("Replacement project or epic; empty clears it"),
				"tag":              stringProperty("Replacement tag; empty clears it"),
				"notes":            stringProperty("Replacement notes; empty clears them"),
			}, []string{"id"}),
			Annotations: annotations(false, false, true),
		},
		{
			Name: "add_subtask", Title: "Add a workboard subtask",
			Description: "Add a concrete step to an existing task. Prefer this over creating a duplicate card when the work belongs to the same outcome.",
			InputSchema: objectSchema(map[string]any{
				"id":               stringProperty("Full task ID"),
				"expectedRevision": map[string]any{"type": "integer", "minimum": 1, "description": "Parent task revision from get_task; conflict means read again before retrying."},
				"title":            stringProperty("Required subtask title"),
				"lane":             enumProperty("Initial subtask lane; defaults to the parent task lane", store.LaneOrder),
			}, []string{"id", "title"}),
			Annotations: annotations(false, false, false),
		},
		{
			Name: "update_subtask", Title: "Update a workboard subtask",
			Description: "Rename a subtask or move its lane independently from its parent as progress occurs. The done field is a compatibility shortcut for moving to or from Done.",
			InputSchema: objectSchema(map[string]any{
				"id":               stringProperty("Full task ID"),
				"expectedRevision": map[string]any{"type": "integer", "minimum": 1, "description": "Parent task revision from get_task; conflict means read again before retrying."},
				"subtaskId":        stringProperty("Full subtask ID"),
				"title":            stringProperty("Replacement subtask title"),
				"lane":             enumProperty("Independent subtask lane", store.LaneOrder),
				"done":             map[string]any{"type": "boolean", "description": "Whether the subtask is complete"},
			}, []string{"id", "subtaskId"}),
			Annotations: annotations(false, false, true),
		},
	}
	for i := range definitions {
		if definitions[i].Name == "create_task" || definitions[i].Name == "update_task" {
			props := definitions[i].InputSchema["properties"].(map[string]any)
			props["links"] = linksSchema()
			props["agentContext"] = map[string]any{"type": "string", "maxLength": 4000, "description": "Agent memo, exposed in local API/history"}
			props["reconcileMode"] = enumProperty("Future reconciliation control", []string{"automatic", "manual"})
		}
	}
	return append(definitions, linkTools()...)
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type": "object", "properties": properties, "additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumProperty(description string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func integerProperty(description string, minimum, maximum int) map[string]any {
	return map[string]any{
		"type": "integer", "description": description, "minimum": minimum, "maximum": maximum,
	}
}

func annotations(readOnly, destructive, idempotent bool) map[string]any {
	return map[string]any{
		"readOnlyHint": readOnly, "destructiveHint": destructive,
		"idempotentHint": idempotent, "openWorldHint": false,
	}
}
