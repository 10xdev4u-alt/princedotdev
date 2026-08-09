package diff

import "testing"

func TestIdentical(t *testing.T) {
	src := "a\nb\nc\n"
	if h := Lines(src, src); len(h) != 0 {
		t.Fatalf("identical input produced hunks: %v", h)
	}
}

func TestPureAddition(t *testing.T) {
	h := Lines("a\nb\n", "a\nX\nb\n")
	if len(h) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(h))
	}
	got := kinds(h[0].Lines)
	want := []string{"ctx", "add", "ctx"}
	if !equal(got, want) {
		t.Fatalf("kinds %v, want %v", got, want)
	}
	if h[0].NewStart != 1 || h[0].NewCount != 3 {
		t.Fatalf("new range %d,%d", h[0].NewStart, h[0].NewCount)
	}
}

func TestPureDeletion(t *testing.T) {
	h := Lines("a\nX\nb\n", "a\nb\n")
	if len(h) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(h))
	}
	got := kinds(h[0].Lines)
	want := []string{"ctx", "del", "ctx"}
	if !equal(got, want) {
		t.Fatalf("kinds %v, want %v", got, want)
	}
}

func TestReplacementMiddle(t *testing.T) {
	h := Lines("a\nb\nc\nd\ne\n", "a\nb\nC\nd\ne\n")
	if len(h) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(h))
	}
	got := kinds(h[0].Lines)
	if !equal(got, []string{"ctx", "ctx", "del", "add", "ctx", "ctx"}) {
		t.Fatalf("kinds %v", got)
	}
	// deleted line is old line 3, added line is new line 3
	var delN, addN int
	for _, l := range h[0].Lines {
		if l.Kind == KindDel {
			delN = l.OldN
		}
		if l.Kind == KindAdd {
			addN = l.NewN
		}
	}
	if delN != 3 || addN != 3 {
		t.Fatalf("del oldN=%d add newN=%d, want 3/3", delN, addN)
	}
}

func TestTwoSeparateChanges(t *testing.T) {
	src := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19\n20\n"
	h := Lines(src, "1\n2\nX\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19\nY\n")
	if len(h) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(h))
	}
}

func TestStats(t *testing.T) {
	h := Lines("a\nb\nc\n", "a\nX\nY\nc\n")
	added, removed := Counts(h)
	if added != 2 || removed != 1 {
		t.Fatalf("added=%d removed=%d, want 2/1", added, removed)
	}
}

func TestEmptyNew(t *testing.T) {
	h := Lines("a\nb\n", "")
	if len(h) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(h))
	}
	added, removed := Counts(h)
	if added != 1 || removed != 2 {
		t.Fatalf("added=%d removed=%d, want 1/2 (empty target is one empty line)", added, removed)
	}
}

func kinds(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Kind
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
