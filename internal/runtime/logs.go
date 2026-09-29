package runtime

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var ansi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

func completeLines(b []byte) []byte {
	if end := bytes.LastIndexByte(b, '\n'); end >= 0 {
		return b[:end+1]
	}
	return nil
}

func Redact(data []byte, secrets []string, limit int, truncated bool) []byte {
	// Remove display controls before matching, so ANSI inserted inside a known
	// secret cannot reveal that secret after the output is rendered.
	clean := func(b []byte) []byte {
		b = ansi.ReplaceAll(b, nil)
		return bytes.Map(func(r rune) rune {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return -1
			}
			return r
		}, b)
	}
	data = clean(data)
	for i, s := range secrets {
		secrets[i] = string(clean([]byte(s)))
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	// A compiled replacement trie avoids per-byte scans across every revision.
	seen := map[string]bool{}
	pairs := []string{}
	for _, s := range secrets {
		if s != "" && !seen[s] {
			seen[s] = true
			pairs = append(pairs, s, "[REDACTED]")
		}
	}
	b := []byte(strings.NewReplacer(pairs...).Replace(string(data)))
	// Output capture can end in the middle of a secret: drop the final line.
	if truncated {
		if end := bytes.LastIndexByte(b, '\n'); end >= 0 {
			b = b[:end+1]
		} else {
			b = nil
		}
	}
	if len(b) > limit {
		b = b[:limit]
	}
	return b
}
