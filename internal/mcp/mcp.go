// Package mcp implements a Model Context Protocol server over stdio
// (newline-delimited JSON-RPC 2.0). It exposes the draftdeck API as tools so
// agents (Claude Code, Codebuff, …) can publish drafts and drive review
// natively. Zero dependencies — the protocol is small enough to speak
// directly.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Version of the server binary.
var Version = "dev"

const protocolVersion = "2024-11-05"

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolCall is the args of a tools/call.
type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// toolResult is the content of a tools/call response.
type toolResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Server talks to the draftdeck HTTP API on behalf of the agent.
type Server struct {
	apiURL string
	apiKey string
	tools  []toolDef
	hc     *http.Client
}

// New builds the MCP server bound to a draftdeck API.
func New(apiURL, apiKey string) *Server {
	return &Server{
		apiURL: strings.TrimRight(apiURL, "/"),
		apiKey: apiKey,
		tools:  tools(),
		hc:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Run reads newline-delimited JSON-RPC from in and writes responses to out.
func (s *Server) Run(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // malformed frame; MCP clients ignore
		}
		resp := s.dispatch(msg)
		if resp == nil {
			continue // notification
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) dispatch(msg rpcMessage) *rpcMessage {
	hasID := len(msg.ID) > 0 && string(msg.ID) != "null"

	switch msg.Method {
	case "initialize":
		return s.reply(msg.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "draftdeck", "version": Version},
		})
	case "ping":
		return s.reply(msg.ID, map[string]any{})
	case "tools/list":
		return s.reply(msg.ID, map[string]any{"tools": s.tools})
	case "tools/call":
		var call toolCall
		_ = json.Unmarshal(msg.Params, &call)
		result, err := s.callTool(call)
		if err != nil {
			return s.reply(msg.ID, toolResult{
				Content: []contentItem{{Type: "text", Text: "Error: " + err.Error()}},
				IsError: true,
			})
		}
		return s.reply(msg.ID, result)
	case "notifications/initialized", "notifications/cancelled", "notifications/progress":
		return nil
	default:
		if !hasID {
			return nil
		}
		return &rpcMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   &rpcError{Code: -32601, Message: "Method not found: " + msg.Method},
		}
	}
}

func (s *Server) reply(id json.RawMessage, result any) *rpcMessage {
	return &rpcMessage{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) callTool(call toolCall) (toolResult, error) {
	var text string
	var err error
	switch call.Name {
	case "upload_draft":
		text, err = s.upload(call.Arguments)
	case "list_drafts":
		text, err = s.listDrafts()
	case "get_draft":
		text, err = s.getDraft(call.Arguments)
	case "list_comments":
		text, err = s.listComments(call.Arguments)
	case "post_comment":
		text, err = s.postComment(call.Arguments)
	case "set_status":
		text, err = s.setStatus(call.Arguments)
	case "list_teams":
		text, err = s.listTeams()
	default:
		err = fmt.Errorf("unknown tool %q", call.Name)
	}
	if err != nil {
		return toolResult{}, err
	}
	return toolResult{Content: []contentItem{{Type: "text", Text: text}}}, nil
}

func (s *Server) needKey() error {
	if s.apiKey == "" {
		return fmt.Errorf("DRAFTDECK_API_KEY is not set")
	}
	return nil
}

func (s *Server) upload(args map[string]any) (string, error) {
	if err := s.needKey(); err != nil {
		return "", err
	}
	html, _ := args["html"].(string)
	if strings.TrimSpace(html) == "" {
		return "", fmt.Errorf("html is required")
	}
	payload := map[string]any{
		"html":        html,
		"filename":    strArg(args, "filename", "draft.html"),
		"description": strArg(args, "description", ""),
		"draftId":     nil,
		"visibility":  nil,
		"teamId":      nil,
	}
	for _, k := range []string{"draftId", "visibility", "teamId"} {
		if v, ok := args[k].(string); ok && v != "" {
			payload[k] = v
		}
	}
	body, err := s.call("POST", "/api/uploads", payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Uploaded %v v%v (%v)\nURL: %v\nRaw: %v\nStatus: %v",
		body["title"], body["versionNumber"], body["draftId"], body["publicUrl"], body["rawUrl"], body["status"]), nil
}

func (s *Server) listDrafts() (string, error) {
	if err := s.needKey(); err != nil {
		return "", err
	}
	body, err := s.call("GET", "/api/drafts", nil)
	if err != nil {
		return "", err
	}
	drafts, _ := body["drafts"].([]any)
	if len(drafts) == 0 {
		return "No drafts yet.", nil
	}
	var b strings.Builder
	for _, raw := range drafts {
		d, _ := raw.(map[string]any)
		fmt.Fprintf(&b, "%v  [%v]  %v · %v · %v\n",
			d["draftId"], d["status"], latestOf(d), d["versionCount"], d["title"])
	}
	return b.String(), nil
}

func (s *Server) getDraft(args map[string]any) (string, error) {
	if err := s.needKey(); err != nil {
		return "", err
	}
	id := strArg(args, "draftId", "")
	if id == "" {
		return "", fmt.Errorf("draftId is required")
	}
	body, err := s.call("GET", "/api/drafts/"+id, nil)
	if err != nil {
		return "", err
	}
	draft, _ := body["draft"].(map[string]any)
	out, _ := json.MarshalIndent(map[string]any{
		"draft":    draft,
		"versions": body["versions"],
		"comments": body["comments"],
	}, "", "  ")
	return string(out), nil
}

func (s *Server) listComments(args map[string]any) (string, error) {
	id := strArg(args, "draftId", "")
	if id == "" {
		return "", fmt.Errorf("draftId is required")
	}
	body, err := s.call("GET", "/api/drafts/"+id+"/comments", nil)
	if err != nil {
		return "", err
	}
	comments, _ := body["comments"].([]any)
	if len(comments) == 0 {
		return "No comments yet.", nil
	}
	var b strings.Builder
	for _, raw := range comments {
		c, _ := raw.(map[string]any)
		fmt.Fprintf(&b, "[v%v] %v: %v\n", c["versionNumber"], c["author"], c["body"])
	}
	return b.String(), nil
}

func (s *Server) postComment(args map[string]any) (string, error) {
	if err := s.needKey(); err != nil {
		return "", err
	}
	id := strArg(args, "draftId", "")
	if id == "" {
		return "", fmt.Errorf("draftId is required")
	}
	bodyText := strArg(args, "body", "")
	if bodyText == "" {
		return "", fmt.Errorf("body is required")
	}
	payload := map[string]any{"body": bodyText}
	if sel := strArg(args, "selector", ""); sel != "" {
		payload["anchor"] = map[string]any{"selector": sel}
	}
	if v, ok := args["versionNumber"].(float64); ok && v > 0 {
		payload["versionNumber"] = int64(v)
	}
	body, err := s.call("POST", "/api/drafts/"+id+"/comments", payload)
	if err != nil {
		return "", err
	}
	c, _ := body["comment"].(map[string]any)
	return fmt.Sprintf("Comment posted by %v (v%v)", c["author"], c["versionNumber"]), nil
}

func (s *Server) setStatus(args map[string]any) (string, error) {
	if err := s.needKey(); err != nil {
		return "", err
	}
	id := strArg(args, "draftId", "")
	status := strArg(args, "status", "")
	if id == "" || status == "" {
		return "", fmt.Errorf("draftId and status are required")
	}
	body, err := s.call("POST", "/api/drafts/"+id+"/status", map[string]any{"status": status})
	if err != nil {
		return "", err
	}
	d, _ := body["draft"].(map[string]any)
	return fmt.Sprintf("%v → %v", d["title"], d["status"]), nil
}

func (s *Server) listTeams() (string, error) {
	if err := s.needKey(); err != nil {
		return "", err
	}
	body, err := s.call("GET", "/api/teams", nil)
	if err != nil {
		return "", err
	}
	teams, _ := body["teams"].([]any)
	if len(teams) == 0 {
		return "No teams yet.", nil
	}
	var b strings.Builder
	for _, raw := range teams {
		t, _ := raw.(map[string]any)
		fmt.Fprintf(&b, "%v  %v\n", t["id"], t["name"])
	}
	return b.String(), nil
}

func latestOf(d map[string]any) any {
	if n, ok := d["latestVersionNumber"].(float64); ok && n > 0 {
		return fmt.Sprintf("v%.0f", n)
	}
	return "—"
}

func strArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

// ---- HTTP client ------------------------------------------------------------

func (s *Server) call(method, endpoint string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.apiURL+endpoint, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := out["error"].(string)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return out, nil
}

// ---- tools schema -------------------------------------------------------------

func tools() []toolDef {
	return []toolDef{
		{
			Name:        "upload_draft",
			Description: "Publish or update an HTML draft (validate locally first).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"html":        map[string]any{"type": "string", "description": "Full HTML document"},
					"filename":    map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"draftId":     map[string]any{"type": "string", "description": "Update this draft (re-upload versions it)"},
					"visibility":  map[string]any{"type": "string", "enum": []string{"public", "unlisted", "team"}},
					"teamId":      map[string]any{"type": "string"},
				},
				"required": []string{"html"},
			},
		},
		{
			Name:        "list_drafts",
			Description: "List drafts on your account and teams.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "get_draft",
			Description: "Full draft detail: metadata, versions with git provenance, comments.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"draftId": map[string]any{"type": "string"}},
				"required":   []string{"draftId"},
			},
		},
		{
			Name:        "list_comments",
			Description: "Read the feedback thread on a draft (works keyless for public/unlisted).",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"draftId": map[string]any{"type": "string"}},
				"required":   []string{"draftId"},
			},
		},
		{
			Name:        "post_comment",
			Description: "Leave anchored feedback on a draft.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"draftId":       map[string]any{"type": "string"},
					"body":          map[string]any{"type": "string"},
					"selector":      map[string]any{"type": "string", "description": "CSS selector anchor"},
					"versionNumber": map[string]any{"type": "number"},
				},
				"required": []string{"draftId", "body"},
			},
		},
		{
			Name:        "set_status",
			Description: "Set review status: draft | in_review | changes_requested | approved.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"draftId": map[string]any{"type": "string"},
					"status":  map[string]any{"type": "string", "enum": []string{"draft", "in_review", "changes_requested", "approved"}},
				},
				"required": []string{"draftId", "status"},
			},
		},
		{
			Name:        "list_teams",
			Description: "List teams you belong to.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}
