package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/10xdev4u-alt/princedotdev/internal/config"
	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// newTestServer boots a full server on a temp data dir (5 GiB budget default).
func newTestServer(t *testing.T, mutate func(*config.Config)) (*Server, *db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Port:                    "0",
		DataDir:                 dir,
		PublicBaseURL:           "http://test.local",
		SessionSecret:           "test-secret",
		MaxHTMLBytes:            512 * 1024,
		StorageBudget:           5 * 1024 * 1024 * 1024,
		UploadRateLimitMax:      1000,
		UploadRateLimitWindowMs: 60000,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, s.db, dir
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func doRaw(t *testing.T, h http.Handler, method, path string, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const testHTML = "<!doctype html><html><head><title>Plan</title></head><body><h1>v1</h1></body></html>"

func TestUploadServeVersioning(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()

	// healthz
	if rec := doRaw(t, h, "GET", "/healthz", ""); rec.Code != 200 {
		t.Fatalf("healthz %d", rec.Code)
	}

	// anonymous upload
	rec, up := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": testHTML, "filename": "plan.html"}, "")
	if rec.Code != 201 {
		t.Fatalf("upload %d: %s", rec.Code, rec.Body.String())
	}
	draftID := up["draftId"].(string)
	if up["versionNumber"].(float64) != 1 {
		t.Fatalf("v1 expected, got %v", up["versionNumber"])
	}

	// raw byte-for-byte
	rec2 := doRaw(t, h, "GET", "/d/"+draftID+"/raw", "")
	if rec2.Code != 200 || rec2.Body.String() != testHTML {
		t.Fatalf("raw mismatch: %d %q", rec2.Code, rec2.Body.String())
	}
	if csp := rec2.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'none'") {
		t.Fatalf("missing CSP: %q", csp)
	}

	// versioned re-upload
	html2 := "<html><head><title>Plan v2</title></head><body><h1>v2</h1></body></html>"
	rec3, up2 := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": html2, "draftId": draftID}, "")
	if rec3.Code != 200 || up2["versionNumber"].(float64) != 2 {
		t.Fatalf("v2 %d %v", rec3.Code, up2)
	}
	if rec := doRaw(t, h, "GET", "/d/"+draftID+"/v/2/raw", ""); rec.Body.String() != html2 {
		t.Fatal("v2 raw mismatch")
	}

	// 404s
	if rec := doRaw(t, h, "GET", "/d/doesnotexist", ""); rec.Code != 404 {
		t.Fatalf("missing draft %d", rec.Code)
	}
	_ = d
}

func TestPolicyReject(t *testing.T) {
	s, _, _ := newTestServer(t, nil)
	rec, out := doJSON(t, s.Handler(), "POST", "/api/uploads", map[string]any{
		"html": `<html><body><iframe src="x"></iframe><script src="https://evil.dev/a.js"></script></body></html>`,
	}, "")
	if rec.Code != 422 {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
	errs, _ := out["errors"].([]any)
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors, got %v", errs)
	}
}

func TestAuthRequired(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	if rec := doRaw(t, h, "GET", "/api/me", ""); rec.Code != 401 {
		t.Fatalf("me without key %d", rec.Code)
	}
	id, err := d.CreateAccount("Maya", "maya@team.dev")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.CreateAPIKey(id, "cli")
	if err != nil {
		t.Fatal(err)
	}
	rec, out := doJSON(t, h, "GET", "/api/me", nil, token)
	if rec.Code != 200 || out["accountName"] != "Maya" {
		t.Fatalf("me with key %d %v", rec.Code, out)
	}
}

func TestCommentsAndStatus(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	id, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, token, _ := d.CreateAPIKey(id, "cli")

	_, up := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": testHTML, "filename": "p.html"}, token)
	draftID := up["draftId"].(string)

	// comment flips to in_review
	rec, c := doJSON(t, h, "POST", "/api/drafts/"+draftID+"/comments",
		map[string]any{"body": "make the heading bigger", "anchor": map[string]any{"selector": "h1"}}, token)
	if rec.Code != 201 || c["comment"].(map[string]any)["author"] != "Maya" {
		t.Fatalf("comment %d %v", rec.Code, c)
	}
	_, detail := doJSON(t, h, "GET", "/api/drafts/"+draftID, nil, token)
	if detail["draft"].(map[string]any)["status"] != "in_review" {
		t.Fatal("expected in_review after comment")
	}

	// approve
	_, st := doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "approved"}, token)
	if st["draft"].(map[string]any)["status"] != "approved" {
		t.Fatal("expected approved")
	}

	// re-upload after approval resets to draft (persisted)
	_, up2 := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": testHTML, "draftId": draftID}, token)
	if up2["status"] != "draft" {
		t.Fatalf("status reset expected draft, got %v", up2["status"])
	}
	_, detail2 := doJSON(t, h, "GET", "/api/drafts/"+draftID, nil, token)
	if detail2["draft"].(map[string]any)["status"] != "draft" {
		t.Fatal("status reset not persisted")
	}

	// invalid status rejected
	if rec := doJSON2(t, h, draftID, token); rec.Code != 400 {
		t.Fatalf("invalid status %d", rec.Code)
	}

	// comments readable anonymously for non-team drafts
	if rec := doRaw(t, h, "GET", "/api/drafts/"+draftID+"/comments", ""); rec.Code != 200 {
		t.Fatalf("anon comments %d", rec.Code)
	}
}

func doJSON2(t *testing.T, h http.Handler, draftID, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec, _ := doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "banana"}, token)
	return rec
}

func TestTeamsPrivacy(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	maya, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, mayaTok, _ := d.CreateAPIKey(maya, "cli")
	ben, _ := d.CreateAccount("Ben", "ben@team.dev")
	_, benTok, _ := d.CreateAPIKey(ben, "cli")

	_, team := doJSON(t, h, "POST", "/api/teams", map[string]any{"name": "Eng"}, mayaTok)
	teamID := team["team"].(map[string]any)["id"].(string)

	// team draft upload + privacy
	_, up := doJSON(t, h, "POST", "/api/uploads",
		map[string]any{"html": testHTML, "teamId": teamID, "visibility": "team"}, mayaTok)
	draftID := up["draftId"].(string)

	if rec := doRaw(t, h, "GET", "/d/"+draftID+"/raw", benTok); rec.Code != 401 {
		t.Fatalf("non-member team raw %d", rec.Code)
	}
	// anonymous team upload rejected
	if rec, _ := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": testHTML, "teamId": teamID}, ""); rec.Code != 401 {
		t.Fatalf("anon team upload %d", rec.Code)
	}

	// add member, then ben can read
	if rec, _ := doJSON(t, h, "POST", "/api/teams/"+teamID+"/members", map[string]any{"email": "ben@team.dev"}, mayaTok); rec.Code != 201 {
		t.Fatalf("add member %d", rec.Code)
	}
	if rec := doRaw(t, h, "GET", "/d/"+draftID+"/raw", benTok); rec.Code != 200 {
		t.Fatalf("member team raw %d", rec.Code)
	}
	// non-owner cannot add members
	if rec, _ := doJSON(t, h, "POST", "/api/teams/"+teamID+"/members", map[string]any{"email": "x@y.dev"}, benTok); rec.Code != 403 {
		t.Fatalf("non-owner add %d", rec.Code)
	}
}

func TestStorageBudget(t *testing.T) {
	s, d, _ := newTestServer(t, func(c *config.Config) { c.StorageBudget = 2048 })
	h := s.Handler()
	id, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, token, _ := d.CreateAPIKey(id, "cli")

	if rec, _ := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": "<html><body>small</body></html>"}, token); rec.Code != 201 {
		t.Fatalf("small upload %d", rec.Code)
	}
	big := "<html><body>" + strings.Repeat("x", 5000) + "</body></html>"
	if rec, _ := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": big}, token); rec.Code != 507 {
		t.Fatalf("over-budget %d", rec.Code)
	}
}

func TestControlPanel(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	id, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, token, _ := d.CreateAPIKey(id, "cli")

	// stats
	_, stats := doJSON(t, h, "GET", "/api/stats", nil, token)
	if stats["storageBudget"].(float64) != 5*1024*1024*1024 {
		t.Fatalf("budget %v", stats["storageBudget"])
	}

	// mint a key
	rec, minted := doJSON(t, h, "POST", "/api/api-keys", map[string]any{"name": "sandbox"}, token)
	if rec.Code != 201 {
		t.Fatalf("mint %d", rec.Code)
	}
	newToken := minted["token"].(string)
	keyID := minted["apiKey"].(map[string]any)["id"].(string)
	if rec := doRaw(t, h, "GET", "/api/me", newToken); rec.Code != 200 {
		t.Fatalf("new key auth %d", rec.Code)
	}

	// revoke it
	if rec := doRaw(t, h, "DELETE", "/api/api-keys/"+keyID, token); rec.Code != 200 {
		t.Fatalf("revoke %d", rec.Code)
	}
	if rec := doRaw(t, h, "GET", "/api/me", newToken); rec.Code != 401 {
		t.Fatalf("revoked key still works: %d", rec.Code)
	}

	// team members: create team, add member, list, remove
	_, team := doJSON(t, h, "POST", "/api/teams", map[string]any{"name": "Eng"}, token)
	teamID := team["team"].(map[string]any)["id"].(string)
	ben, _ := d.CreateAccount("Ben", "ben@team.dev")
	doJSON(t, h, "POST", "/api/teams/"+teamID+"/members", map[string]any{"email": "ben@team.dev"}, token)
	_, members := doJSON(t, h, "GET", "/api/teams/"+teamID+"/members", nil, token)
	if len(members["members"].([]any)) != 2 {
		t.Fatalf("members %v", members)
	}
	if rec := doRaw(t, h, "DELETE", "/api/teams/"+teamID+"/members/"+ben, token); rec.Code != 200 {
		t.Fatalf("remove member %d", rec.Code)
	}
	_, members2 := doJSON(t, h, "GET", "/api/teams/"+teamID+"/members", nil, token)
	if len(members2["members"].([]any)) != 1 {
		t.Fatalf("members after remove %v", members2)
	}
}

func TestBackupRestore(t *testing.T) {
	s, d, dir := newTestServer(t, nil)
	h := s.Handler()
	id, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, token, _ := d.CreateAPIKey(id, "cli")
	doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": testHTML, "filename": "p.html"}, token)

	// live backup (server still running)
	backupPath := filepath.Join(dir, "snapshot.db")
	if err := d.Backup(backupPath); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// restore into a fresh instance
	restoreDir := t.TempDir()
	raw, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restoreDir, "draftdeck.db"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := New(config.Config{
		DataDir:       restoreDir,
		PublicBaseURL: "http://restored.local",
		StorageBudget: 5 * 1024 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rec, out := doJSON(t, s2.Handler(), "GET", "/api/me", nil, token)
	if rec.Code != 200 || out["accountName"] != "Maya" {
		t.Fatalf("restored auth %d %v", rec.Code, out)
	}
	_, drafts := doJSON(t, s2.Handler(), "GET", "/api/drafts", nil, token)
	if len(drafts["drafts"].([]any)) != 1 {
		t.Fatalf("restored drafts %v", drafts)
	}
}

func TestWebhookDelivery(t *testing.T) {
	var mu sync.Mutex
	var received []map[string]any
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p map[string]any
		_ = json.Unmarshal(body, &p)
		mu.Lock()
		received = append(received, p)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer receiver.Close()

	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	id, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, token, _ := d.CreateAPIKey(id, "cli")

	// status-only webhook
	rec, wh := doJSON(t, h, "POST", "/api/webhooks", map[string]any{
		"name": "status channel", "kind": "discord", "url": receiver.URL, "events": []string{"status"},
	}, token)
	if rec.Code != 201 {
		t.Fatalf("create webhook %d %v", rec.Code, wh)
	}
	whID := wh["webhook"].(map[string]any)["id"].(string)

	// all-events webhook
	if rec, _ := doJSON(t, h, "POST", "/api/webhooks", map[string]any{
		"name": "full channel", "kind": "discord", "url": receiver.URL,
	}, token); rec.Code != 201 {
		t.Fatalf("create full webhook %d", rec.Code)
	}

	// invalid inputs rejected
	if rec, _ := doJSON(t, h, "POST", "/api/webhooks", map[string]any{
		"name": "bad", "kind": "discord", "url": "ftp://nope",
	}, token); rec.Code != 400 {
		t.Fatalf("bad url %d", rec.Code)
	}
	if rec, _ := doJSON(t, h, "POST", "/api/webhooks", map[string]any{
		"name": "bad", "kind": "pager", "url": receiver.URL,
	}, token); rec.Code != 400 {
		t.Fatalf("bad kind %d", rec.Code)
	}

	// upload (triggers only the full webhook), comment (full only),
	// status (both).
	_, up := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": testHTML, "filename": "p.html"}, token)
	draftID := up["draftId"].(string)
	doJSON(t, h, "POST", "/api/drafts/"+draftID+"/comments",
		map[string]any{"body": "make the heading bigger", "anchor": map[string]any{"selector": "h1"}}, token)
	doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "in_review"}, token)

	// expect 4 deliveries: full×3 + status-only×1
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 4 {
		t.Fatalf("expected 4 deliveries, got %d: %v", len(received), received)
	}

	events := map[string]int{}
	var statusPayload map[string]any
	for _, p := range received {
		ev, _ := p["event"].(string)
		events[ev]++
		if ev == "status" {
			statusPayload = p
		}
		if ev == "comment" {
			if _, ok := p["comment"].(map[string]any); ok {
				continue
			}
			// discord embeds carry the body in the description
			embeds := p["embeds"].([]any)
			desc, _ := embeds[0].(map[string]any)["description"].(string)
			if !strings.Contains(desc, "make the heading bigger") {
				t.Fatalf("comment body missing: %v", p)
			}
		}
	}
	if events["upload"] != 1 || events["comment"] != 1 || events["status"] != 2 {
		t.Fatalf("event counts %v", events)
	}

	// discord embed formatting on a status delivery
	embeds, ok := statusPayload["embeds"].([]any)
	if !ok || len(embeds) == 0 {
		t.Fatalf("discord embed missing: %v", statusPayload)
	}
	author := embeds[0].(map[string]any)["author"].(map[string]any)["name"].(string)
	if !strings.Contains(author, "Maya") {
		t.Fatalf("actor %v", author)
	}
	fields := embeds[0].(map[string]any)["fields"].([]any)
	statusFound := false
	for _, f := range fields {
		if f.(map[string]any)["name"] == "Status" && f.(map[string]any)["value"] == "In review" {
			statusFound = true
		}
	}
	if !statusFound {
		t.Fatalf("status field missing: %v", embeds[0])
	}

	// delivery result recorded on the webhook row
	whRow, err := d.FindWebhook(whID)
	if err != nil || whRow == nil || whRow.LastStatus != 200 {
		t.Fatalf("last status %+v err %v", whRow, err)
	}

	// list + delete
	_, listed := doJSON(t, h, "GET", "/api/webhooks", nil, token)
	if n := len(listed["webhooks"].([]any)); n != 2 {
		t.Fatalf("list webhooks %d", n)
	}
	if rec := doRaw(t, h, "DELETE", "/api/webhooks/"+whID, token); rec.Code != 200 {
		t.Fatalf("delete webhook %d", rec.Code)
	}
	_, listed2 := doJSON(t, h, "GET", "/api/webhooks", nil, token)
	if n := len(listed2["webhooks"].([]any)); n != 1 {
		t.Fatalf("list after delete %d", n)
	}
}

func TestInvites(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	ownerID, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, ownerTok, _ := d.CreateAPIKey(ownerID, "cli")

	_, team := doJSON(t, h, "POST", "/api/teams", map[string]any{"name": "Eng"}, ownerTok)
	teamID := team["team"].(map[string]any)["id"].(string)

	// non-owner cannot invite
	otherID, _ := d.CreateAccount("Ben", "ben@team.dev")
	_, otherTok, _ := d.CreateAPIKey(otherID, "cli")
	doJSON(t, h, "POST", "/api/teams/"+teamID+"/members", map[string]any{"email": "ben@team.dev"}, ownerTok)
	if rec, _ := doJSON(t, h, "POST", "/api/teams/"+teamID+"/invites", map[string]any{"email": "x@team.dev"}, otherTok); rec.Code != 403 {
		t.Fatalf("non-owner invite %d", rec.Code)
	}

	// owner creates invite for a brand-new email
	rec, out := doJSON(t, h, "POST", "/api/teams/"+teamID+"/invites", map[string]any{"email": "New@Team.dev"}, ownerTok)
	if rec.Code != 201 {
		t.Fatalf("create invite %d %v", rec.Code, out)
	}
	inviteURL := out["inviteUrl"].(string)
	token := inviteURL[strings.LastIndex(inviteURL, "/")+1:]

	// resolve
	if rec, resolved := doJSON(t, h, "GET", "/api/invites/"+token, nil, ""); rec.Code != 200 ||
		resolved["teamName"] != "Eng" || resolved["email"] != "new@team.dev" {
		t.Fatalf("resolve invite %d %v", rec.Code, resolved)
	}

	// accept for a brand-new account
	rec, accepted := doJSON(t, h, "POST", "/api/invites/"+token+"/accept", map[string]any{"name": "Newbie"}, "")
	if rec.Code != 200 {
		t.Fatalf("accept invite %d %v", rec.Code, accepted)
	}
	if accepted["teamName"] != "Eng" || accepted["email"] != "new@team.dev" {
		t.Fatalf("accepted %v", accepted)
	}
	newKey := accepted["apiKey"].(string)
	if newKey == "" {
		t.Fatalf("no key minted")
	}
	// new account is a team member (owner + Ben + Newbie)
	_, members := doJSON(t, h, "GET", "/api/teams/"+teamID+"/members", nil, ownerTok)
	if n := len(members["members"].([]any)); n != 3 {
		t.Fatalf("members after accept %d", n)
	}
	// new key works
	if rec := doRaw(t, h, "GET", "/api/me", newKey); rec.Code != 200 {
		t.Fatalf("new key me %d", rec.Code)
	}

	// invite is single-use
	if rec, _ := doJSON(t, h, "POST", "/api/invites/"+token+"/accept", nil, ""); rec.Code != 404 {
		t.Fatalf("re-accept %d", rec.Code)
	}

	// invite for an existing account joins without creating a new one
	rec, out2 := doJSON(t, h, "POST", "/api/teams/"+teamID+"/invites", map[string]any{"email": "ben@team.dev"}, ownerTok)
	if rec.Code != 201 {
		t.Fatalf("invite existing %d", rec.Code)
	}
	url2 := out2["inviteUrl"].(string)
	token2 := url2[strings.LastIndex(url2, "/")+1:]
	_, accepted2 := doJSON(t, h, "POST", "/api/invites/"+token2+"/accept", nil, "")
	if accepted2["accountId"] != otherID {
		t.Fatalf("existing account not reused: %v", accepted2)
	}
}

func TestActivityFeed(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	mayaID, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, mayaTok, _ := d.CreateAPIKey(mayaID, "cli")
	zoeyID, _ := d.CreateAccount("Zoey", "zoey@team.dev")
	_, zoeyTok, _ := d.CreateAPIKey(zoeyID, "cli")

	_, up := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": testHTML, "filename": "p.html"}, mayaTok)
	draftID := up["draftId"].(string)

	// comment with a mention of zoey
	doJSON(t, h, "POST", "/api/drafts/"+draftID+"/comments",
		map[string]any{"body": "please fix @zoey@team.dev — see anchor"}, mayaTok)
	// status change
	doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "in_review"}, mayaTok)

	// Maya's feed: upload, comment, status (3 entries)
	_, feed := doJSON(t, h, "GET", "/api/activity", nil, mayaTok)
	items := feed["activity"].([]any)
	if len(items) != 3 {
		t.Fatalf("maya feed %d", len(items))
	}
	// Zoey's feed: only the mention (1 entry)
	_, feedZ := doJSON(t, h, "GET", "/api/activity", nil, zoeyTok)
	itemsZ := feedZ["activity"].([]any)
	if len(itemsZ) != 1 {
		t.Fatalf("zoey feed %d", len(itemsZ))
	}
	first := itemsZ[0].(map[string]any)
	if first["kind"] != "mention" || !strings.Contains(first["body"].(string), "mentioned you") {
		t.Fatalf("mention %v", first)
	}
	// unread counts
	_, feedM := doJSON(t, h, "GET", "/api/activity", nil, mayaTok)
	if feedM["unread"].(float64) != 3 {
		t.Fatalf("unread %v", feedM["unread"])
	}
	// mark read
	if rec, _ := doJSON(t, h, "POST", "/api/activity/read", nil, mayaTok); rec.Code != 200 {
		t.Fatalf("mark read %d", rec.Code)
	}
	_, feedM2 := doJSON(t, h, "GET", "/api/activity", nil, mayaTok)
	if feedM2["unread"].(float64) != 0 {
		t.Fatalf("unread after read %v", feedM2["unread"])
	}
}

func TestRolesAndApprovals(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	ownerID, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, ownerTok, _ := d.CreateAPIKey(ownerID, "cli")
	benID, _ := d.CreateAccount("Ben", "ben@team.dev")
	_, benTok, _ := d.CreateAPIKey(benID, "cli")
	zoeyID, _ := d.CreateAccount("Zoey", "zoey@team.dev")
	_, zoeyTok, _ := d.CreateAPIKey(zoeyID, "cli")

	_, team := doJSON(t, h, "POST", "/api/teams", map[string]any{"name": "Eng"}, ownerTok)
	teamID := team["team"].(map[string]any)["id"].(string)
	doJSON(t, h, "POST", "/api/teams/"+teamID+"/members", map[string]any{"email": "ben@team.dev"}, ownerTok)
	doJSON(t, h, "POST", "/api/teams/"+teamID+"/members", map[string]any{"email": "zoey@team.dev"}, ownerTok)

	// owner promotes Ben to admin
	if rec, _ := doJSON(t, h, "PUT", "/api/teams/"+teamID+"/members/"+benID, map[string]any{"role": "admin"}, ownerTok); rec.Code != 200 {
		t.Fatalf("promote %d", rec.Code)
	}
	// admin can set the approval gate
	if rec, _ := doJSON(t, h, "PUT", "/api/teams/"+teamID, map[string]any{"requiredApprovals": 2}, benTok); rec.Code != 200 {
		t.Fatalf("admin gate %d", rec.Code)
	}
	// plain member cannot
	if rec, _ := doJSON(t, h, "PUT", "/api/teams/"+teamID, map[string]any{"requiredApprovals": 1}, zoeyTok); rec.Code != 403 {
		t.Fatalf("member gate %d", rec.Code)
	}
	// admin cannot change roles (owner only)
	if rec, _ := doJSON(t, h, "PUT", "/api/teams/"+teamID+"/members/"+zoeyID, map[string]any{"role": "admin"}, benTok); rec.Code != 403 {
		t.Fatalf("admin role change %d", rec.Code)
	}
	// owner cannot demote another owner
	_, team2 := doJSON(t, h, "POST", "/api/teams", map[string]any{"name": "Ops"}, ownerTok)
	team2ID := team2["team"].(map[string]any)["id"].(string)
	if rec, _ := doJSON(t, h, "PUT", "/api/teams/"+team2ID+"/members/"+ownerID, map[string]any{"role": "member"}, ownerTok); rec.Code != 200 {
		t.Fatalf("demote owner %d", rec.Code)
	}
	_, members := doJSON(t, h, "GET", "/api/teams/"+team2ID+"/members", nil, ownerTok)
	if members["members"].([]any)[0].(map[string]any)["role"] != "owner" {
		t.Fatalf("owner role changed: %v", members)
	}

	// approval gate: team draft needs 2 approvals
	_, up := doJSON(t, h, "POST", "/api/uploads",
		map[string]any{"html": testHTML, "teamId": teamID, "visibility": "team"}, ownerTok)
	draftID := up["draftId"].(string)

	// Ben approves → still in_review (1/2)
	rec, out := doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "approved"}, benTok)
	if rec.Code != 200 || out["approved"].(bool) {
		t.Fatalf("ben approve %v %v", rec.Code, out)
	}
	if out["draft"].(map[string]any)["status"] != "in_review" {
		t.Fatalf("status %v", out["draft"])
	}
	app := out["draft"].(map[string]any)["approvals"].(map[string]any)
	if app["count"].(float64) != 1 || app["required"].(float64) != 2 {
		t.Fatalf("approvals %v", app)
	}

	// Ben approves again → still 1 (idempotent)
	doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "approved"}, benTok)
	_, det := doJSON(t, h, "GET", "/api/drafts/"+draftID, nil, ownerTok)
	app2 := det["draft"].(map[string]any)["approvals"].(map[string]any)
	if app2["count"].(float64) != 1 {
		t.Fatalf("dup approval counted %v", app2)
	}

	// Zoey approves → gate reached, approved
	rec, out2 := doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "approved"}, zoeyTok)
	if rec.Code != 200 || !out2["approved"].(bool) {
		t.Fatalf("zoey approve %v %v", rec.Code, out2)
	}
	if out2["draft"].(map[string]any)["status"] != "approved" {
		t.Fatalf("final status %v", out2["draft"])
	}
}

func TestTagsAndFilters(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	h := s.Handler()
	id, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, token, _ := d.CreateAPIKey(id, "cli")

	_, up := doJSON(t, h, "POST", "/api/uploads",
		map[string]any{"html": testHTML, "filename": "a.html", "description": "Q3 retry automation", "tags": []string{"q3", "agents"}}, token)
	draftA := up["draftId"].(string)
	_, up2 := doJSON(t, h, "POST", "/api/uploads",
		map[string]any{"html": testHTML, "filename": "b.html", "description": "Billing dunning", "tags": []string{"q3"}}, token)
	draftB := up2["draftId"].(string)
	doJSON(t, h, "POST", "/api/drafts/"+draftB+"/status", map[string]any{"status": "approved"}, token)

	// tags in list responses
	_, all := doJSON(t, h, "GET", "/api/drafts", nil, token)
	drafts := all["drafts"].([]any)
	if len(drafts) != 2 {
		t.Fatalf("all %d", len(drafts))
	}
	found := map[string][]any{}
	for _, raw := range drafts {
		it := raw.(map[string]any)
		found[it["draftId"].(string)] = it["tags"].([]any)
	}
	if len(found[draftA]) != 2 || len(found[draftB]) != 1 {
		t.Fatalf("tags %v", found)
	}

	// filter by status
	_, byStatus := doJSON(t, h, "GET", "/api/drafts?status=approved", nil, token)
	if n := len(byStatus["drafts"].([]any)); n != 1 {
		t.Fatalf("status filter %d", n)
	}
	// filter by tag
	_, byTag := doJSON(t, h, "GET", "/api/drafts?tag=agents", nil, token)
	if n := len(byTag["drafts"].([]any)); n != 1 {
		t.Fatalf("tag filter %d", n)
	}
	// search
	_, byQ := doJSON(t, h, "GET", "/api/drafts?q=dunning", nil, token)
	if n := len(byQ["drafts"].([]any)); n != 1 {
		t.Fatalf("q filter %d", n)
	}

	// replace tags via PUT
	if rec, out := doJSON(t, h, "PUT", "/api/drafts/"+draftA+"/tags", map[string]any{"tags": []string{"q3", "recovery"}}, token); rec.Code != 200 || len(out["tags"].([]any)) != 2 {
		t.Fatalf("put tags %d %v", rec.Code, out)
	}
	_, detail := doJSON(t, h, "GET", "/api/drafts/"+draftA, nil, token)
	if tags := detail["tags"].([]any); len(tags) != 2 {
		t.Fatalf("detail tags %v", tags)
	}
	// cleared tags drop out of tag filter
	if rec, _ := doJSON(t, h, "PUT", "/api/drafts/"+draftA+"/tags", map[string]any{"tags": []string{}}, token); rec.Code != 200 {
		t.Fatalf("clear tags %d", rec.Code)
	}
	_, byTag2 := doJSON(t, h, "GET", "/api/drafts?tag=agents", nil, token)
	if n := len(byTag2["drafts"].([]any)); n != 0 {
		t.Fatalf("tag after clear %d", n)
	}
}

func TestDraftDiffAPI(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	id, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, token, _ := d.CreateAPIKey(id, "cli")
	h := s.Handler()

	_, up := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": "<h1>One</h1>\n<p>alpha</p>\n", "filename": "p.html"}, token)
	draftID := up["draftId"].(string)

	// second version with an edit + an addition
	_, up2 := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": "<h1>One!</h1>\n<p>alpha</p>\n<p>beta</p>\n", "filename": "p.html", "draftId": draftID}, token)
	if up2["draftId"].(string) != draftID {
		t.Fatalf("second upload created a new draft: %v", up2["draftId"])
	}

	rec, body := doJSON(t, h, "GET", "/api/drafts/"+draftID+"/diff?from=1&to=2", nil, token)
	if rec.Code != 200 {
		t.Fatalf("diff status %d: %v", rec.Code, body)
	}
	if body["from"].(float64) != 1 || body["to"].(float64) != 2 {
		t.Fatalf("range %v -> %v", body["from"], body["to"])
	}
	stats := body["stats"].(map[string]any)
	if stats["added"].(float64) != 2 || stats["removed"].(float64) != 1 {
		t.Fatalf("stats %v", stats)
	}
	hunks := body["hunks"].([]any)
	if len(hunks) == 0 {
		t.Fatal("expected hunks")
	}
	// the edit should appear as a deletion then an addition in order
	hunk := hunks[0].(map[string]any)
	lines := hunk["lines"].([]any)
	var kinds []string
	for _, l := range lines {
		kinds = append(kinds, l.(map[string]any)["kind"].(string))
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "del,add") {
		t.Fatalf("edit not del-before-add: %v", kinds)
	}

	// same-version comparison is rejected
	rec, body = doJSON(t, h, "GET", "/api/drafts/"+draftID+"/diff?from=2&to=2", nil, token)
	if rec.Code != 400 {
		t.Fatalf("same-version status %d", rec.Code)
	}

	// no auth -> 401
	rec, _ = doJSON(t, h, "GET", "/api/drafts/"+draftID+"/diff?from=1&to=2", nil, "")
	if rec.Code != 401 {
		t.Fatalf("unauthenticated diff status %d", rec.Code)
	}
}

func TestReviewerAssignmentGate(t *testing.T) {
	s, d, _ := newTestServer(t, nil)
	ownerID, _ := d.CreateAccount("Maya", "maya@team.dev")
	_, ownerTok, _ := d.CreateAPIKey(ownerID, "cli")
	benID, _ := d.CreateAccount("Ben", "ben@team.dev")
	_, benTok, _ := d.CreateAPIKey(benID, "cli")
	carolID, _ := d.CreateAccount("Carol", "carol@team.dev")
	_, carolTok, _ := d.CreateAPIKey(carolID, "cli")
	h := s.Handler()

	team, _ := d.CreateTeam("Core", ownerID)
	teamID := team.ID
	_ = d.AddTeamMember(teamID, benID, "member")
	_ = d.AddTeamMember(teamID, carolID, "member")
	_ = d.UpdateTeamApprovals(teamID, 2)

	// upload a team draft
	_, up := doJSON(t, h, "POST", "/api/uploads", map[string]any{"html": testHTML, "filename": "r.html", "teamId": teamID}, ownerTok)
	draftID := up["draftId"].(string)

	// assign Ben + Carol as reviewers
	rec, body := doJSON(t, h, "PUT", "/api/drafts/"+draftID+"/reviewers", map[string]any{"accountIds": []string{benID, carolID}}, ownerTok)
	if rec.Code != 200 {
		t.Fatalf("assign %d: %v", rec.Code, body)
	}

	// a non-reviewer (Ben is a reviewer; use a third member check) — actually
	// make Maya (owner, not reviewer) approve: shouldn't count toward the gate.
	rec, body = doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "approved"}, ownerTok)
	if rec.Code != 200 || body["approved"] != false {
		t.Fatalf("owner approval should not clear gate: %d %v", rec.Code, body)
	}

	// Ben approves -> still 1/2
	rec, body = doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "approved"}, benTok)
	if rec.Code != 200 || body["approved"] != false {
		t.Fatalf("1/2 should hold: %d %v", rec.Code, body)
	}

	// Carol approves -> 2/2 -> approved
	rec, body = doJSON(t, h, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": "approved"}, carolTok)
	if rec.Code != 200 || body["approved"] != true {
		t.Fatalf("2/2 should approve: %d %v", rec.Code, body)
	}

	// detail exposes reviewers with approval state
	rec, body = doJSON(t, h, "GET", "/api/drafts/"+draftID, nil, ownerTok)
	revs := body["draft"].(map[string]any)["reviewers"].([]any)
	if len(revs) != 2 {
		t.Fatalf("reviewers %v", revs)
	}
	approved := 0
	for _, rv := range revs {
		if rv.(map[string]any)["approved"] == true {
			approved++
		}
	}
	if approved != 2 {
		t.Fatalf("approved reviewers %d, want 2", approved)
	}

	// non-member cannot be assigned
	_, body = doJSON(t, h, "PUT", "/api/drafts/"+draftID+"/reviewers", map[string]any{"accountIds": []string{"acct_outsider"}}, ownerTok)
	if rec := body["ok"]; rec == true {
		t.Fatalf("outsider accepted: %v", body)
	}

	// plain member cannot assign reviewers
	rec, _ = doJSON(t, h, "PUT", "/api/drafts/"+draftID+"/reviewers", map[string]any{"accountIds": []string{benID}}, benTok)
	if rec.Code != 403 {
		t.Fatalf("member assign status %d, want 403", rec.Code)
	}
}
