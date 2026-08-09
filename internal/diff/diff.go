// Package diff computes a line-based diff between two draft versions.
// It is a self-contained LCS implementation (no external deps) that trims
// common prefix/suffix before diffing, so typical deck changes — a few
// edited lines in a mostly-identical file — stay fast.
package diff

import "strings"

// Line kinds.
const (
	KindCtx = "ctx"
	KindAdd = "add"
	KindDel = "del"
)

// Line is a single rendered line of a hunk. OldN/NewN are 1-based line
// numbers in the respective version; 0 means the line is absent from that
// version (additions have OldN 0, deletions have NewN 0).
type Line struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	OldN int    `json:"oldN"`
	NewN int    `json:"newN"`
}

// Hunk is a contiguous run of changed lines with leading/trailing context.
type Hunk struct {
	OldStart int    `json:"oldStart"`
	OldCount int    `json:"oldCount"`
	NewStart int    `json:"newStart"`
	NewCount int    `json:"newCount"`
	Lines    []Line `json:"lines"`
}

// Stats summarizes the size of a diff.
type Stats struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

const (
	contextLines = 3
	maxDP        = 1500 // LCS table size cap; beyond this we diff as replace-all
)

// Lines diffs oldText against newText and returns hunks with context.
// The final newline terminator is not a diffable line.
func Lines(oldText, newText string) []Hunk {
	old := strings.Split(strings.TrimSuffix(oldText, "\n"), "\n")
	neu := strings.Split(strings.TrimSuffix(newText, "\n"), "\n")
	return hunks(old, neu)
}

type edit struct {
	kind byte // ' ' match, 'd' delete, 'a' add
	text string
}

func hunks(old, neu []string) []Hunk {
	// Trim the common prefix and suffix; diff only the middle.
	pre := 0
	for pre < len(old) && pre < len(neu) && old[pre] == neu[pre] {
		pre++
	}
	suf := 0
	for suf < len(old)-pre && suf < len(neu)-pre &&
		old[len(old)-1-suf] == neu[len(neu)-1-suf] {
		suf++
	}
	midOld := old[pre : len(old)-suf]
	midNew := neu[pre : len(neu)-suf]
	ops := computeEdits(midOld, midNew)

	lines := assemble(old[:pre], ops, old[len(old)-suf:], neu[len(neu)-suf:])
	return toHunks(lines)
}

// computeEdits returns the edit script for midOld -> midNew.
func computeEdits(midOld, midNew []string) []edit {
	switch {
	case len(midOld) == 0:
		out := make([]edit, len(midNew))
		for i, t := range midNew {
			out[i] = edit{'a', t}
		}
		return out
	case len(midNew) == 0:
		out := make([]edit, len(midOld))
		for i, t := range midOld {
			out[i] = edit{'d', t}
		}
		return out
	case len(midOld)*len(midNew) > maxDP*maxDP:
		// Too big to DP: report the whole middle as replaced.
		out := make([]edit, 0, len(midOld)+len(midNew))
		for _, t := range midOld {
			out = append(out, edit{'d', t})
		}
		for _, t := range midNew {
			out = append(out, edit{'a', t})
		}
		return out
	}

	n, m := len(midOld), len(midNew)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if midOld[i-1] == midNew[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	// Backtrack from the bottom-right corner.
	out := make([]edit, 0, n+m)
	i, j := n, m
	for i > 0 && j > 0 {
		switch {
		case midOld[i-1] == midNew[j-1]:
			out = append(out, edit{' ', midOld[i-1]})
			i--
			j--
		case dp[i][j-1] >= dp[i-1][j]:
			out = append(out, edit{'a', midNew[j-1]})
			j--
		default:
			out = append(out, edit{'d', midOld[i-1]})
			i--
		}
	}
	for ; i > 0; i-- {
		out = append(out, edit{'d', midOld[i-1]})
	}
	for ; j > 0; j-- {
		out = append(out, edit{'a', midNew[j-1]})
	}
	// Reverse: backtracking produced the script in reverse order.
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}

// assemble merges prefix, the edit script, and suffix into numbered lines.
func assemble(pre []string, ops []edit, sufOld, sufNew []string) []Line {
	lines := make([]Line, 0, len(pre)+len(ops)+len(sufOld))
	oldN, newN := 1, 1
	ctx := func(text string) {
		lines = append(lines, Line{KindCtx, text, oldN, newN})
		oldN++
		newN++
	}
	for _, t := range pre {
		ctx(t)
	}
	for _, e := range ops {
		switch e.kind {
		case ' ':
			ctx(e.text)
		case 'd':
			lines = append(lines, Line{KindDel, e.text, oldN, 0})
			oldN++
		case 'a':
			lines = append(lines, Line{KindAdd, e.text, 0, newN})
			newN++
		}
	}
	for _, t := range sufOld {
		ctx(t)
	}
	return lines
}

// toHunks groups changed lines into hunks with up to contextLines of context.
func toHunks(lines []Line) []Hunk {
	var changed []int
	for i, l := range lines {
		if l.Kind != KindCtx {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	var out []Hunk
	start := changed[0]
	prev := changed[0]
	for _, i := range changed[1:] {
		if i-prev > 2*contextLines {
			out = append(out, makeHunk(lines, start, prev))
			start = i
		}
		prev = i
	}
	out = append(out, makeHunk(lines, start, prev))
	return out
}

func makeHunk(lines []Line, first, last int) Hunk {
	lo := first - contextLines
	if lo < 0 {
		lo = 0
	}
	hi := last + contextLines + 1
	if hi > len(lines) {
		hi = len(lines)
	}
	chunk := lines[lo:hi]
	h := Hunk{Lines: chunk}
	for _, l := range chunk {
		if l.OldN > 0 {
			if h.OldCount == 0 {
				h.OldStart = l.OldN
			}
			h.OldCount++
		}
		if l.NewN > 0 {
			if h.NewCount == 0 {
				h.NewStart = l.NewN
			}
			h.NewCount++
		}
	}
	if h.OldCount == 0 {
		h.OldStart = 1
	}
	if h.NewCount == 0 {
		h.NewStart = 1
	}
	return h
}

// Counts returns the number of added and removed lines across hunks.
func Counts(hunks []Hunk) (added, removed int) {
	for _, h := range hunks {
		for _, l := range h.Lines {
			switch l.Kind {
			case KindAdd:
				added++
			case KindDel:
				removed++
			}
		}
	}
	return added, removed
}
