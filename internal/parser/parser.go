// Package parser turns the structured stdout of frappe-monitor-collect.sh
// into typed Go values. The Tokenize function is the lowest layer: it splits
// the raw text into named sections of key→value strings, without
// interpreting any value semantics. Section-specific converters (e.g.
// ServerFromSections in Task 3) consume the Output and produce typed
// metric structs.
package parser

import (
	"fmt"
	"strings"
)

// Section is a named ###-delimited block of key=value lines from the
// collector script. Multi-instance metrics are encoded as keys with
// inline label braces — e.g. `disk_used_bytes{mount="/"}` — and KVs
// preserves them verbatim. The downstream converter is responsible
// for splitting the labels.
type Section struct {
	Name string            // "META", "SERVER", or for Phase 3 "BENCH:foo", "SITE:foo:bar"
	KVs  map[string]string // key→value, both kept as raw strings
}

// Output is the tokenized representation of one collector cycle.
type Output struct {
	Sections []Section
}

// Tokenize splits the collector script's stdout into Sections. It returns
// an error on:
//   - missing ###END terminator
//   - content lines outside any section
//   - duplicate section names within the same input
//   - lines that aren't ###section markers, ###END, blank, or key=value
func Tokenize(input string) (Output, error) {
	var out Output
	var current *Section
	hadEnd := false
	seen := map[string]bool{}

	lines := strings.Split(input, "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		lineNo := i + 1

		if line == "" {
			continue
		}

		if line == "###END" {
			if current != nil {
				out.Sections = append(out.Sections, *current)
				current = nil
			}
			hadEnd = true
			break
		}

		if strings.HasPrefix(line, "###") {
			if current != nil {
				out.Sections = append(out.Sections, *current)
			}
			name := strings.TrimPrefix(line, "###")
			if name == "" {
				return Output{}, fmt.Errorf("line %d: empty section name", lineNo)
			}
			if seen[name] {
				return Output{}, fmt.Errorf("line %d: duplicate section %q", lineNo, name)
			}
			seen[name] = true
			current = &Section{Name: name, KVs: map[string]string{}}
			continue
		}

		if current == nil {
			return Output{}, fmt.Errorf("line %d: content outside section: %q", lineNo, line)
		}

		key, value, err := splitKV(line)
		if err != nil {
			return Output{}, fmt.Errorf("line %d: %w", lineNo, err)
		}
		current.KVs[key] = value
	}

	if !hadEnd {
		return Output{}, fmt.Errorf("missing ###END marker")
	}
	return out, nil
}

// splitKV finds the `=` at brace-depth zero and splits the line there.
// Lines like `disk_used_bytes{mount="/"}=1234` contain an `=` inside the
// label braces; the value separator is the `=` outside the braces.
func splitKV(line string) (key, value string, err error) {
	depth := 0
	eq := -1
scan:
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '=':
			if depth == 0 {
				eq = i
				break scan
			}
		}
	}
	if eq < 0 {
		return "", "", fmt.Errorf("malformed (no '=' at brace-depth zero): %q", line)
	}
	return line[:eq], line[eq+1:], nil
}
