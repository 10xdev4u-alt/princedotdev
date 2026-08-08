package mcp

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/10xdev4u-alt/princedotdev/internal/config"
	"github.com/10xdev4u-alt/princedotdev/internal/db"
	"github.com/10xdev4u-alt/princedotdev/internal/server"
)

// newAPI boots a real draftdeck server and returns its URL + an API key.
func newAPI(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()

	// Create the account before the server opens the same data dir (WAL
	// makes the second connection safe).
	d, err := db.Open(config.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.CreateAccount("Agent", "agent@team.dev")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.CreateAPIKey(id, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		DataDir:       dir,
		PublicBaseURL: "http://test.local",
		StorageBudget: 5 * 1024 * 1024 * 1024,
	}
	s, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return hs.URL, token
}

func session(t *testing.T, apiURL, apiKey string, lines ...string) []map[string]any {
	t.Helper()
	srv := New(apiURL, apiKey)
	var in, out bytes.Buffer
	for _, l := range lines {
		in.WriteString(l + "\n")
	}
	if err := srv.Run(&in, &out); err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func resultText(msgs []map[string]any, i int) string {
	return msgs[i]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
}

func TestInitializeAndToolsList(t *testing.T) {
	apiURL, key := newAPI(t)
	msgs := session(t, apiURL, key,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(msgs))
	}
	info := msgs[0]["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != "draftdeck" {
		t.Fatalf("serverInfo %v", info)
	}
	tools := msgs[1]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, raw := range tools {
		names[raw.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"upload_draft", "list_drafts", "get_draft", "list_comments", "post_comment", "set_status", "list_teams"} {
		if !names[want] {
			t.Fatalf("missing tool %s in %v", want, names)
		}
	}
}

func TestToolRoundTrip(t *testing.T) {
	apiURL, key := newAPI(t)
	html := `<html><head><title>MCP Plan</title></head><body>hi</body></html>`

	msgs := session(t, apiURL, key,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"upload_draft","arguments":{"html":`+jsonStr(html)+`}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_drafts","arguments":{}}}`,
	)
	upText := resultText(msgs, 0)
	if !strings.Contains(upText, "Uploaded MCP Plan v1") {
		t.Fatalf("upload text %q", upText)
	}
	draftID := upText[strings.Index(upText, "(")+1 : strings.Index(upText, ")")]
	if !strings.Contains(resultText(msgs, 1), draftID) {
		t.Fatalf("list missing draft: %q", resultText(msgs, 1))
	}

	msgs2 := session(t, apiURL, key,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"post_comment","arguments":{"draftId":"`+draftID+`","body":"ship it"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"set_status","arguments":{"draftId":"`+draftID+`","status":"approved"}}}`,
	)
	if !strings.Contains(resultText(msgs2, 0), "Comment posted by Agent") {
		t.Fatalf("comment %q", resultText(msgs2, 0))
	}
	if !strings.Contains(resultText(msgs2, 1), "approved") {
		t.Fatalf("status %q", resultText(msgs2, 1))
	}
}

func TestBadToolIsError(t *testing.T) {
	apiURL, _ := newAPI(t)
	msgs := session(t, apiURL, "",
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"explode","arguments":{}}}`,
	)
	r := msgs[0]["result"].(map[string]any)
	if r["isError"] != true {
		t.Fatalf("expected isError, got %v", r)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
