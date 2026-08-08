package policy

import (
	"strings"
	"testing"
)

func TestValidDoc(t *testing.T) {
	doc := `<!doctype html><html><head><title>Plan</title><style>body{color:#88C0D0}</style></head>
<body><h1>RecoverKit Q3</h1><p>Plan text</p><script>console.log(1)</script></body></html>`
	r := Validate([]byte(doc), 0)
	if !r.OK {
		t.Fatalf("expected ok, got %v", r.Errors)
	}
	if r.Title != "Plan" {
		t.Fatalf("title %q", r.Title)
	}
}

func TestBlockedTags(t *testing.T) {
	for _, tag := range []string{"form", "iframe", "object", "embed", "base", "link"} {
		doc := "<html><body><" + tag + "></" + tag + "></body></html>"
		if r := Validate([]byte(doc), 0); r.OK {
			t.Fatalf("expected <%s> to be blocked", tag)
		}
	}
}

func TestScriptRules(t *testing.T) {
	if r := Validate([]byte(`<html><body><script src="https://x.dev/a.js"></script></body></html>`), 0); r.OK {
		t.Fatal("external script should be blocked")
	}
	if r := Validate([]byte(`<html><body><script type="module">x()</script></body></html>`), 0); r.OK {
		t.Fatal("module script type should be blocked")
	}
	if r := Validate([]byte(`<html><body><script>alert(1)</script></body></html>`), 0); !r.OK {
		t.Fatalf("inline classic script should pass: %v", r.Errors)
	}
}

func TestUnsafeUrls(t *testing.T) {
	cases := []string{
		`<a href="javascript:alert(1)">x</a>`,
		`<img src="javascript:void(0)">`,
		"<a href=\"  java\tscript:alert(1)\">x</a>",
	}
	for _, doc := range cases {
		if r := Validate([]byte("<html><body>"+doc+"</body></html>"), 0); r.OK {
			t.Fatalf("expected block for %q", doc)
		}
	}
}

func TestEventHandlersAndMetaRefresh(t *testing.T) {
	if r := Validate([]byte(`<html><body onclick="x()"></body></html>`), 0); r.OK {
		t.Fatal("inline handler should be blocked")
	}
	if r := Validate([]byte(`<html><head><meta http-equiv="refresh" content="0"></head></html>`), 0); r.OK {
		t.Fatal("meta refresh should be blocked")
	}
	if r := Validate([]byte(`<html><body><div style="background:url(javascript:evil())"></div></body></html>`), 0); r.OK {
		t.Fatal("unsafe CSS should be blocked")
	}
}

func TestEmptyAndOversize(t *testing.T) {
	if r := Validate([]byte("   \n "), 0); r.OK {
		t.Fatal("empty doc should fail")
	}
	if r := Validate([]byte("<html><body>"+strings.Repeat("x", 600)+"</body></html>"), 100); r.OK {
		t.Fatal("oversize doc should fail")
	}
}

func TestExternalImageHosts(t *testing.T) {
	doc := `<html><body><img src="https://img.example.com/a.png"><img src="/local.png"><img src="data:image/png;base64,xx"></body></html>`
	r := Validate([]byte(doc), 0)
	if !r.OK {
		t.Fatalf("expected ok: %v", r.Errors)
	}
	if len(r.ExternalImageHosts) != 1 || r.ExternalImageHosts[0] != "img.example.com" {
		t.Fatalf("hosts %v", r.ExternalImageHosts)
	}
}

func TestNoTitleWarning(t *testing.T) {
	r := Validate([]byte(`<html><body><p>hi</p></body></html>`), 0)
	if !r.OK {
		t.Fatalf("expected ok: %v", r.Errors)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "title") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected title warning, got %v", r.Warnings)
	}
}
