// draftdeck-cli is the publishing CLI: upload HTML drafts, read comments,
// drive the review status, and manage teams. Zero dependencies — the npx
// wrapper downloads this single static binary.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/10xdev4u-alt/princedotdev/internal/policy"
)

// Version is stamped at build time (default: dev).
var Version = "dev"

const (
	defaultAPIURL = "http://localhost:4000"
	cliName       = "draftdeck"
)

type cliError struct{ msg string }

func (e *cliError) Error() string { return e.msg }

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(0)
	}
	cmd := args[0]
	rest := args[1:]

	if cmd == "version" || cmd == "--version" || cmd == "-v" {
		fmt.Println(cliName + " " + Version)
		return
	}
	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		usage()
		return
	}

	var err error
	switch cmd {
	case "auth":
		err = cmdAuth(rest)
	case "upload":
		err = cmdUpload(rest)
	case "list":
		err = cmdList(rest)
	case "comments":
		err = cmdComments(rest)
	case "status":
		err = cmdStatus(rest)
	case "teams":
		err = cmdTeams(rest)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`draftdeck — publish static HTML drafts and manage team review.

Usage:
  draftdeck auth set <api-key> [--api-url <url>]   save credentials
  draftdeck auth whoami [--api-url <url>]          check credentials
  draftdeck upload <file> [--draft <id>] [--new]   publish or update a draft
                [--description <text>] [--visibility public|unlisted|team]
                [--team <team-id>] [--api-url <url>]
  draftdeck list [--json] [--api-url <url>]        list drafts
  draftdeck comments <draft-id> [--post <text>]    read or post feedback
                [--selector <css>] [--version <n>]
  draftdeck status <draft-id> <status>             draft|in_review|changes_requested|approved
  draftdeck teams                                  list teams
  draftdeck teams create --name <name>
  draftdeck teams members --team <id> --email <email>
  draftdeck version
`)
}

// parseFlags reorders args so all flags precede positionals, then parses
// with fs. Go's stdlib flag stops at the first non-flag arg; this restores
// the commander-style "flags anywhere" UX (upload file --team x).
// boolFlags lists flags that never consume the following arg.
func parseFlags(fs *flag.FlagSet, boolFlags map[string]bool, args []string) ([]string, error) {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := strings.TrimLeft(a, "-")
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && !boolFlags[name] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1]) // flag value
				i++
			}
		} else {
			positionals = append(positionals, a)
		}
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return positionals, nil
}

// ---- auth -------------------------------------------------------------------

func cmdAuth(args []string) error {
	if len(args) == 0 {
		return &cliError{"auth requires a subcommand: set | whoami"}
	}
	switch args[0] {
	case "set":
		fs := flag.NewFlagSet("auth set", flag.ContinueOnError)
		apiURL := fs.String("api-url", "", "Override the API base URL")
		positionals, err := parseFlags(fs, nil, args[1:])
		if err != nil {
			return err
		}
		key := ""
		if len(positionals) > 0 {
			key = positionals[0]
		}
		if key == "" {
			return &cliError{"Usage: draftdeck auth set <api-key>"}
		}
		return saveCredentials(key, *apiURL)
	case "whoami":
		fs := flag.NewFlagSet("auth whoami", flag.ContinueOnError)
		apiURL := fs.String("api-url", "", "Override the API base URL")
		if _, err := parseFlags(fs, nil, args[1:]); err != nil {
			return err
		}
		auth := readAuth(*apiURL, true)
		body, err := apiCall(auth.apiURL, "GET", "/api/me", nil, auth.apiKey)
		if err != nil {
			return err
		}
		fmt.Printf("Account: %v (%v)\n", body["accountName"], body["accountId"])
		fmt.Printf("API key: %v (%v)\n", body["apiKeyName"], body["apiKeyId"])
		return nil
	default:
		return &cliError{"auth requires a subcommand: set | whoami"}
	}
}

// ---- upload -----------------------------------------------------------------

func cmdUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	draftID := fs.String("draft", "", "Update a specific draft")
	isNew := fs.Bool("new", false, "Always create a new draft")
	description := fs.String("description", "", "Short description for the draft")
	visibility := fs.String("visibility", "", "public, unlisted, or team (default: unlisted)")
	teamID := fs.String("team", "", "Attach the draft to a team (implies team visibility)")
	apiURL := fs.String("api-url", "", "Override the API base URL")
	positionals, err := parseFlags(fs, map[string]bool{"new": true}, args)
	if err != nil {
		return err
	}
	file := ""
	if len(positionals) > 0 {
		file = positionals[0]
	}
	if file == "" {
		return &cliError{"Usage: draftdeck upload <file>"}
	}
	resolved, err := filepath.Abs(file)
	if err != nil {
		return &cliError{"Could not resolve file: " + file}
	}
	auth := readAuth(*apiURL, false)

	html, err := os.ReadFile(resolved)
	if err != nil {
		return &cliError{"File does not exist: " + resolved}
	}
	val := policy.Validate(html, 0)
	if !val.OK {
		return &cliError{"HTML failed validation:\n- " + strings.Join(val.Errors, "\n- ")}
	}

	// Update-by-path: remember the draft id for this file so re-uploads
	// version the same draft unless --new or --draft overrides.
	knownDraftID := ""
	if st := readDrafts(); st != nil {
		if f, ok := st.Files[resolved]; ok {
			knownDraftID = f.DraftID
		}
	}
	target := ""
	if *isNew {
		target = ""
	} else if *draftID != "" {
		target = *draftID
	} else {
		target = knownDraftID
	}

	vis := *visibility
	if *teamID != "" {
		vis = "team"
	}
	meta := collectGitMetadata(filepath.Dir(resolved))
	payload := map[string]any{
		"html":        string(html),
		"filename":    path.Base(resolved),
		"draftId":     nullIfEmpty(target),
		"description": nullIfEmpty(*description),
		"visibility":  nullIfEmpty(vis),
		"teamId":      nullIfEmpty(*teamID),
		"metadata": map[string]any{
			"cliVersion":       Version,
			"gitBranch":        nullIfEmpty(meta.Branch),
			"gitCommitSha":     nullIfEmpty(meta.CommitSHA),
			"gitCommitSubject": nullIfEmpty(meta.CommitSubject),
			"gitDirty":         meta.Dirty,
		},
	}
	body, err := apiCall(auth.apiURL, "POST", "/api/uploads", payload, auth.apiKey)
	if err != nil {
		return err
	}

	st := readDrafts()
	if st == nil {
		st = &draftState{Files: map[string]fileState{}}
	}
	if st.Files == nil {
		st.Files = map[string]fileState{}
	}
	st.Files[resolved] = fileState{
		DraftID:             str(body["draftId"]),
		PublicURL:           str(body["publicUrl"]),
		RawURL:              str(body["rawUrl"]),
		LatestVersionNumber: num(body["versionNumber"]),
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
	}
	_ = writeJSON(draftsPath(), st, 0o600)

	if target != "" {
		fmt.Println("Updated draft")
	} else {
		fmt.Println("Uploaded draft")
	}
	fmt.Println("URL: " + str(body["publicUrl"]))
	fmt.Println("Raw HTML: " + str(body["rawUrl"]))
	fmt.Println("Draft ID: " + str(body["draftId"]))
	fmt.Printf("Version: %v · status: %v\n", num(body["versionNumber"]), str(body["status"]))
	for _, w := range val.Warnings {
		fmt.Fprintln(os.Stderr, "Warning: "+w)
	}
	return nil
}

// ---- list -------------------------------------------------------------------

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "Print raw JSON")
	apiURL := fs.String("api-url", "", "Override the API base URL")
	if _, err := parseFlags(fs, map[string]bool{"json": true}, args); err != nil {
		return err
	}
	auth := readAuth(*apiURL, true)
	body, err := apiCall(auth.apiURL, "GET", "/api/drafts", nil, auth.apiKey)
	if err != nil {
		return err
	}
	drafts, _ := body["drafts"].([]any)
	if *asJSON {
		b, _ := json.MarshalIndent(drafts, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(drafts) == 0 {
		fmt.Println("No drafts yet. Publish one with: draftdeck upload <file>")
		return nil
	}
	fmt.Printf("Drafts (%d)\n\n", len(drafts))
	for _, raw := range drafts {
		d, _ := raw.(map[string]any)
		title := str(d["title"])
		if title == "" {
			title = "Untitled Draft"
		}
		fmt.Println(title)
		vc := num(d["versionCount"])
		plural := "versions"
		if vc == 1 {
			plural = "version"
		}
		fmt.Printf("  %v · %v %s · %v · %s\n",
			latest(d), vc, plural, str(d["status"]), timeAgo(str(d["updatedAt"])))
		fmt.Println("  " + str(d["publicUrl"]))
		if desc := str(d["description"]); desc != "" {
			fmt.Println("  " + desc)
		}
		fmt.Println("")
	}
	return nil
}

// ---- comments ---------------------------------------------------------------

func cmdComments(args []string) error {
	fs := flag.NewFlagSet("comments", flag.ContinueOnError)
	post := fs.String("post", "", "Post a comment with this body")
	selector := fs.String("selector", "", "Anchor the comment to a CSS selector")
	versionN := fs.Int("version", 0, "Comment on a specific version (default: latest)")
	apiURL := fs.String("api-url", "", "Override the API base URL")
	positionals, err := parseFlags(fs, nil, args)
	if err != nil {
		return err
	}
	draftID := ""
	if len(positionals) > 0 {
		draftID = positionals[0]
	}
	if draftID == "" {
		return &cliError{"Usage: draftdeck comments <draft-id>"}
	}
	auth := readAuth(*apiURL, false)

	if *post != "" {
		payload := map[string]any{
			"body":          *post,
			"anchor":        nil,
			"versionNumber": nil,
		}
		if *selector != "" {
			payload["anchor"] = map[string]any{"selector": *selector}
		}
		if *versionN > 0 {
			payload["versionNumber"] = *versionN
		}
		body, err := apiCall(auth.apiURL, "POST", "/api/drafts/"+draftID+"/comments", payload, auth.apiKey)
		if err != nil {
			return err
		}
		c, _ := body["comment"].(map[string]any)
		fmt.Printf("Comment posted by %v (v%v)\n", str(c["author"]), num(c["versionNumber"]))
		return nil
	}

	body, err := apiCall(auth.apiURL, "GET", "/api/drafts/"+draftID+"/comments", nil, auth.apiKey)
	if err != nil {
		return err
	}
	comments, _ := body["comments"].([]any)
	if len(comments) == 0 {
		fmt.Println("No comments yet.")
		return nil
	}
	for _, raw := range comments {
		c, _ := raw.(map[string]any)
		label := ""
		if a := anchorOf(c["anchor"]); a != "" {
			label = " @ " + a
		}
		fmt.Printf("[v%v] %v (%v)%v\n", num(c["versionNumber"]), str(c["author"]), str(c["createdAt"]), label)
		fmt.Printf("  %v\n\n", str(c["body"]))
	}
	return nil
}

// ---- status -----------------------------------------------------------------

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	apiURL := fs.String("api-url", "", "Override the API base URL")
	positionals, err := parseFlags(fs, nil, args)
	if err != nil {
		return err
	}
	draftID := ""
	status := ""
	if len(positionals) > 0 {
		draftID = positionals[0]
	}
	if len(positionals) > 1 {
		status = positionals[1]
	}
	if draftID == "" || status == "" {
		return &cliError{"Usage: draftdeck status <draft-id> <status>"}
	}
	allowed := map[string]bool{"draft": true, "in_review": true, "changes_requested": true, "approved": true}
	if !allowed[status] {
		return &cliError{`Invalid status "` + status + `". Use one of: draft, in_review, changes_requested, approved`}
	}
	auth := readAuth(*apiURL, true)
	body, err := apiCall(auth.apiURL, "POST", "/api/drafts/"+draftID+"/status", map[string]any{"status": status}, auth.apiKey)
	if err != nil {
		return err
	}
	d, _ := body["draft"].(map[string]any)
	fmt.Printf("%v → %v\n", str(d["title"]), str(d["status"]))
	return nil
}

// ---- teams ------------------------------------------------------------------

func cmdTeams(args []string) error {
	fs := flag.NewFlagSet("teams", flag.ContinueOnError)
	name := fs.String("name", "", "Team name (for create)")
	teamID := fs.String("team", "", "Team id (for members)")
	email := fs.String("email", "", "Member email (for members)")
	apiURL := fs.String("api-url", "", "Override the API base URL")
	positionals, err := parseFlags(fs, nil, args)
	if err != nil {
		return err
	}
	action := ""
	if len(positionals) > 0 {
		action = positionals[0]
	}
	auth := readAuth(*apiURL, true)

	switch action {
	case "create":
		if *name == "" {
			return &cliError{"--name is required to create a team."}
		}
		body, err := apiCall(auth.apiURL, "POST", "/api/teams", map[string]any{"name": *name}, auth.apiKey)
		if err != nil {
			return err
		}
		t, _ := body["team"].(map[string]any)
		fmt.Printf("Team created: %v (%v)\n", str(t["name"]), str(t["id"]))
		return nil
	case "members":
		if *teamID == "" || *email == "" {
			return &cliError{"--team and --email are required to add a member."}
		}
		body, err := apiCall(auth.apiURL, "POST", "/api/teams/"+*teamID+"/members", map[string]any{"email": *email}, auth.apiKey)
		if err != nil {
			return err
		}
		m, _ := body["member"].(map[string]any)
		fmt.Printf("Added %v (%v) to the team.\n", str(m["name"]), str(m["email"]))
		return nil
	default:
		body, err := apiCall(auth.apiURL, "GET", "/api/teams", nil, auth.apiKey)
		if err != nil {
			return err
		}
		teams, _ := body["teams"].([]any)
		for _, raw := range teams {
			t, _ := raw.(map[string]any)
			fmt.Printf("%v  %v\n", str(t["id"]), str(t["name"]))
		}
		return nil
	}
}

// ---- api client ---------------------------------------------------------------

func apiCall(apiURL, method, endpoint string, payload any, apiKey string) (map[string]any, error) {
	url := strings.TrimRight(apiURL, "/") + endpoint
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, &cliError{"Request failed: " + err.Error()}
	}
	req.Header.Set("User-Agent", cliName+"/"+Version)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &cliError{"Request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := str(out["error"])
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		if errs, ok := out["errors"].([]any); ok && len(errs) > 0 {
			details := make([]string, 0, len(errs))
			for _, e := range errs {
				details = append(details, str(e))
			}
			msg += "\n- " + strings.Join(details, "\n- ")
		}
		return nil, &cliError{msg}
	}
	return out, nil
}

// ---- local state ---------------------------------------------------------------

type fileState struct {
	DraftID             string `json:"draftId"`
	PublicURL           string `json:"publicUrl"`
	RawURL              string `json:"rawUrl"`
	LatestVersionNumber int64  `json:"latestVersionNumber"`
	UpdatedAt           string `json:"updatedAt"`
}

type draftState struct {
	Files map[string]fileState `json:"files"`
}

type credentials struct {
	APIKey    string `json:"apiKey"`
	UpdatedAt string `json:"updatedAt"`
}

type cliConfig struct {
	APIURL string `json:"apiUrl"`
}

type authInfo struct {
	apiURL string
	apiKey string
}

func deckDir() string {
	dir := os.Getenv("DRAFTDECK_HOME")
	if dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".draftdeck")
}

func ensureDir() error { return os.MkdirAll(deckDir(), 0o700) }

func configPath() string { return filepath.Join(deckDir(), "config.json") }
func credsPath() string  { return filepath.Join(deckDir(), "credentials.json") }
func draftsPath() string { return filepath.Join(deckDir(), "drafts.json") }

func readAuth(apiURLOverride string, requireKey bool) authInfo {
	cfg := cliConfig{}
	_ = readJSON(configPath(), &cfg)
	creds := credentials{}
	_ = readJSON(credsPath(), &creds)

	apiURL := os.Getenv("DRAFTDECK_API_URL")
	if apiURLOverride != "" {
		apiURL = apiURLOverride
	}
	if apiURL == "" {
		apiURL = cfg.APIURL
	}
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	apiKey := os.Getenv("DRAFTDECK_API_KEY")
	if apiKey == "" {
		apiKey = creds.APIKey
	}
	if requireKey && apiKey == "" {
		fmt.Fprintln(os.Stderr, "Missing API key. Run: draftdeck auth set <api-key>")
		os.Exit(1)
	}
	return authInfo{apiURL: apiURL, apiKey: apiKey}
}

func saveCredentials(apiKey, apiURLOverride string) error {
	if err := ensureDir(); err != nil {
		return err
	}
	if apiURLOverride != "" {
		cfg := cliConfig{}
		_ = readJSON(configPath(), &cfg)
		cfg.APIURL = strings.TrimRight(apiURLOverride, "/")
		if err := writeJSON(configPath(), cfg, 0o600); err != nil {
			return err
		}
	}
	return writeJSON(credsPath(), credentials{APIKey: apiKey, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}, 0o600)
}

func readDrafts() *draftState {
	st := &draftState{}
	if err := readJSON(draftsPath(), st); err != nil {
		return nil
	}
	return st
}

func readJSON(file string, into any) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func writeJSON(file string, value any, mode os.FileMode) error {
	if err := ensureDir(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, append(b, '\n'), mode); err != nil {
		return err
	}
	return os.Chmod(file, mode)
}

// ---- git metadata ---------------------------------------------------------------

type gitMeta struct {
	Branch        string
	CommitSHA     string
	CommitSubject string
	Dirty         *bool
}

func collectGitMetadata(cwd string) gitMeta {
	return gitMeta{
		Branch:        gitOut(cwd, "rev-parse", "--abbrev-ref", "HEAD"),
		CommitSHA:     gitOut(cwd, "rev-parse", "HEAD"),
		CommitSubject: gitOut(cwd, "log", "-1", "--format=%s"),
		Dirty:         gitDirty(cwd),
	}
}

func gitOut(cwd string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitDirty(cwd string) *bool {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	dirty := len(strings.TrimSpace(string(out))) > 0
	return &dirty
}

// ---- misc ------------------------------------------------------------------------

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func latest(d map[string]any) string {
	if n := num(d["latestVersionNumber"]); n > 0 {
		return "v" + strconv.FormatInt(n, 10)
	}
	return "v—"
}

func anchorOf(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if sel, ok := m["selector"].(string); ok && sel != "" {
		return sel
	}
	if x, ok := m["x"].(float64); ok {
		if y, ok2 := m["y"].(float64); ok2 {
			return "(" + strconv.FormatInt(int64(x), 10) + ", " + strconv.FormatInt(int64(y), 10) + ")"
		}
	}
	return str(m["note"])
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func timeAgo(value string) string {
	if value == "" {
		return "unknown"
	}
	// SQLite yields "YYYY-MM-DD HH:MM:SS" (UTC).
	t, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		return "unknown"
	}
	seconds := int64(time.Since(t.UTC()).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	type unit struct {
		name string
		secs int64
	}
	units := []unit{{"year", 31536000}, {"month", 2592000}, {"week", 604800},
		{"day", 86400}, {"hour", 3600}, {"minute", 60}}
	for _, u := range units {
		if amount := seconds / u.secs; amount >= 1 {
			suffix := ""
			if amount > 1 {
				suffix = "s"
			}
			return strconv.FormatInt(amount, 10) + " " + u.name + suffix + " ago"
		}
	}
	return "just now"
}
