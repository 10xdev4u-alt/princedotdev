// Package policy ports the draftdeck HTML upload policy from the Node
// implementation: static HTML with inline CSS only. No script sources, no
// forms/iframes/embeds, no JS URLs, no meta-refresh. Drafts are served
// byte-for-byte to every client, so this upload-time check is the safety
// boundary of the whole product.
package policy

import (
	"bytes"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// MaxBytes is the default maximum upload size for a single HTML document.
const MaxBytes = 512 * 1024

// MaxDepth is the maximum allowed nesting depth of the document tree.
const MaxDepth = 512

var blockedTags = map[string]bool{
	"form": true, "iframe": true, "object": true, "embed": true,
	"applet": true, "base": true, "link": true,
}

var urlAttrs = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true,
	"poster": true, "srcdoc": true, "xlink:href": true,
}

var allowedScriptTypes = map[string]bool{
	"": true, "text/javascript": true, "application/javascript": true,
}

// Result is the outcome of validating an HTML document.
type Result struct {
	OK       bool
	Errors   []string
	Warnings []string
	Title    string
	// ExternalImageHosts lists the hostnames referenced by <img src>.
	ExternalImageHosts []string
}

// Validate checks html against the upload policy. maxBytes <= 0 means MaxBytes.
func Validate(doc []byte, maxBytes int) Result {
	res := Result{Errors: []string{}, Warnings: []string{}}
	if maxBytes <= 0 {
		maxBytes = MaxBytes
	}

	if len(bytes.TrimSpace(doc)) == 0 {
		res.Errors = append(res.Errors, "HTML document is empty.")
		return res
	}
	if len(doc) > maxBytes {
		res.Errors = append(res.Errors, "HTML document is "+itoa(len(doc))+" bytes; maximum is "+itoa(maxBytes)+" bytes.")
	}

	root, err := html.Parse(bytes.NewReader(doc))
	if err != nil {
		res.Errors = append(res.Errors, "HTML document could not be parsed.")
		return res
	}

	seen := map[string]bool{}
	addErr := func(msg string) {
		if !seen[msg] {
			seen[msg] = true
			res.Errors = append(res.Errors, msg)
		}
	}
	addWarn := func(msg string) {
		if !seen["w:"+msg] {
			seen["w:"+msg] = true
			res.Warnings = append(res.Warnings, msg)
		}
	}

	imgHosts := map[string]bool{}
	titleFound := false

	type item struct {
		node  *html.Node
		depth int
	}
	stack := []item{{node: root, depth: 0}}
	tooDeep := false

	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := it.node

		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)

			if blockedTags[tag] {
				addErr("Blocked <" + tag + "> tag found.")
			}

			if tag == "script" {
				var scriptType string
				hasSrc := false
				for _, a := range n.Attr {
					k := strings.ToLower(a.Key)
					if k == "src" {
						hasSrc = true
					}
					if k == "type" {
						scriptType = strings.ToLower(strings.TrimSpace(a.Val))
					}
				}
				if hasSrc {
					addErr("External script sources are not allowed.")
				}
				if !allowedScriptTypes[scriptType] {
					addErr("Unsupported script type \"" + scriptType + "\" found.")
				}
			}

			for _, a := range n.Attr {
				name := strings.ToLower(a.Key)
				value := strings.TrimSpace(a.Val)

				if strings.HasPrefix(name, "on") {
					addErr("Blocked inline event handler attribute \"" + name + "\" found.")
				}
				if name == "srcdoc" {
					addErr("Blocked \"srcdoc\" attribute found.")
				}
				if urlAttrs[name] {
					normalized := strings.ToLower(stripControlWhitespace(value))
					if strings.HasPrefix(normalized, "javascript:") ||
						strings.HasPrefix(normalized, "vbscript:") ||
						strings.HasPrefix(normalized, "file:") {
						addErr("Blocked unsafe URL in \"" + name + "\" attribute.")
					}
				}
				if name == "style" && unsafeCSS(value) {
					addErr("Blocked unsafe inline CSS.")
				}
			}

			if tag == "meta" {
				for _, a := range n.Attr {
					if strings.ToLower(a.Key) == "http-equiv" &&
						strings.ToLower(strings.TrimSpace(a.Val)) == "refresh" {
						addErr("Blocked meta refresh tag found.")
					}
				}
			}

			if tag == "img" {
				for _, a := range n.Attr {
					if strings.ToLower(a.Key) == "src" {
						if host := externalHost(a.Val); host != "" {
							imgHosts[host] = true
						}
					}
				}
			}

			if tag == "title" && !titleFound {
				if t := strings.TrimSpace(collectText(n)); t != "" {
					if len(t) > 140 {
						t = t[:140]
					}
					res.Title = t
					titleFound = true
				}
			}
		}

		if it.depth >= MaxDepth {
			tooDeep = true
			continue
		}
		for c := n.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, item{node: c, depth: it.depth + 1})
		}
	}

	if tooDeep {
		addErr("HTML is nested more than " + itoa(MaxDepth) + " levels deep.")
	}
	if !titleFound {
		addWarn("No <title> found; a generic title will be used.")
	}

	res.OK = len(res.Errors) == 0
	for h := range imgHosts {
		res.ExternalImageHosts = append(res.ExternalImageHosts, h)
	}
	sort.Strings(res.ExternalImageHosts)
	return res
}

func collectText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func stripControlWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= 0x20 {
			return -1
		}
		return r
	}, s)
}

func unsafeCSS(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "expression(") ||
		strings.Contains(lower, "behavior:") ||
		strings.Contains(lower, "url(javascript:")
}

func externalHost(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return ""
	}
	candidate := raw
	if strings.HasPrefix(raw, "//") {
		candidate = "https:" + raw
	}
	// Only http(s) counts as external; data:, relative paths, etc. return "".
	lower := strings.ToLower(candidate)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return ""
	}
	rest := lower
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.Index(rest, "@"); i >= 0 { // userinfo
		rest = rest[i+1:]
	}
	if i := strings.Index(rest, ":"); i >= 0 { // port
		rest = rest[:i]
	}
	if rest == "" || strings.ContainsAny(rest, " \t") {
		return ""
	}
	return strings.TrimSuffix(rest, ".")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
