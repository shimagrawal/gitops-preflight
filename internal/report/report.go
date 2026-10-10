// Package report prints validation results: one section per check, one line
// per resource, problems indented underneath.
package report

import (
	"fmt"
	"io"
	"strings"
)

// Status of a line.
type Status int

const (
	OK Status = iota
	Fail
	Skip
	Warn // passes, with notes
)

// Line is one resource (or task, or rendered app) and its outcome.
type Line struct {
	Status  Status
	Text    string   // e.g. "Stage/oci-flow/dev" or "Deployment/hello: schema violation"
	Source  string   // e.g. "kargo/stages.yaml" or "dev/hello"
	Details []string // problems, printed under the line
}

// Section is one check.
type Section struct {
	Title   string
	Lines   []Line
	Summary string // optional, e.g. "7 resources found in 4 files - Valid: 7, ..."
}

// Failed reports whether any line failed.
func (s Section) Failed() bool {
	for _, l := range s.Lines {
		if l.Status == Fail {
			return true
		}
	}
	return false
}

// Print writes sections in the grouped ✓/✗ format.
func Print(w io.Writer, sections []Section) {
	for _, s := range sections {
		fmt.Fprintln(w, s.Title)
		if len(s.Lines) == 0 && s.Summary == "" {
			fmt.Fprintln(w, "  (nothing to check)")
		}
		for _, l := range s.Lines {
			mark := map[Status]string{OK: "✓", Fail: "✗", Skip: "-", Warn: "!"}[l.Status]
			line := "  " + mark + " " + l.Text
			if l.Source != "" {
				line += "   (" + l.Source + ")"
			}
			fmt.Fprintln(w, line)
			for _, d := range l.Details {
				fmt.Fprintln(w, "      - "+d)
			}
		}
		if s.Summary != "" {
			fmt.Fprintln(w, "  Summary: "+s.Summary)
		}
	}
}

// Verdict prints the final line and reports whether everything passed.
func Verdict(w io.Writer, sections []Section) bool {
	for _, s := range sections {
		if s.Failed() {
			fmt.Fprintln(w, "\n✗ Validation failed (see ✗ above)")
			return false
		}
	}
	fmt.Fprintln(w, "\n✓ All resources valid")
	return true
}

// Plural returns "1 step" / "2 steps".
func Plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Reason turns a flux-schema reason code into words: "schema-violation" → "schema violation".
func Reason(code string) string { return strings.ReplaceAll(code, "-", " ") }
