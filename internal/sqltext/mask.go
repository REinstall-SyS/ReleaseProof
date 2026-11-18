// Package sqltext supplies a small lexical mask shared by SQL-only script
// validation and heuristic warnings. It does not parse PostgreSQL statements.
package sqltext

import (
	"regexp"
	"strings"
)

var dollarTag = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)

// Mask replaces comments, string contents and quoted identifier contents with
// spaces while preserving byte positions and line endings. The bool is false
// when a quote or block comment is unterminated. Identifier quote boundaries are
// retained for the caller's heuristic patterns.
//
// Ordinary strings follow standard_conforming_strings=on. Only E'...' strings
// use backslash escaping. Changing that PostgreSQL setting inside a script is
// outside this lexer's contract. SQL files remain trusted executable inputs;
// rejecting psql metacommands does not make them a SQL sandbox.
func Mask(input string) (string, bool) {
	out := []byte(input)
	blank := func(start, end int) {
		for j := start; j < end; j++ {
			if out[j] != '\n' && out[j] != '\r' {
				out[j] = ' '
			}
		}
	}
	// PostgreSQL concatenates quoted strings separated only by whitespace with a
	// newline, preserving E-string escape rules across the continuation.
	continuable, newline, previousEscape := false, false, false
	for i := 0; i < len(input); {
		if strings.HasPrefix(input[i:], "--") {
			end := strings.IndexAny(input[i:], "\r\n")
			if end < 0 {
				end = len(input)
			} else {
				end += i
			}
			blank(i, end)
			i = end
			continue
		}
		if strings.HasPrefix(input[i:], "/*") {
			start, depth := i, 1
			i += 2
			for i < len(input) && depth > 0 {
				if strings.HasPrefix(input[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(input[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if continuable && strings.ContainsAny(input[start:i], "\r\n") {
				newline = true
			}
			blank(start, i)
			if depth != 0 {
				return string(out), false
			}
			continue
		}
		if input[i] == '\'' {
			escape := i > 0 && (input[i-1] == 'e' || input[i-1] == 'E') && (i == 1 || !identifierByte(input[i-2]))
			if continuable && newline {
				escape = previousEscape
			}
			start := i
			i++
			closed := false
			for i < len(input) {
				if escape && input[i] == '\\' && i+1 < len(input) {
					i += 2
					continue
				}
				if input[i] == '\'' {
					if i+1 < len(input) && input[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			blank(start, i)
			if !closed {
				return string(out), false
			}
			continuable, newline, previousEscape = true, false, escape
			continue
		}
		if input[i] == '"' {
			continuable = false
			start := i
			i++
			closed := false
			for i < len(input) {
				if input[i] == '"' {
					if i+1 < len(input) && input[i+1] == '"' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return string(out), false
			}
			blank(start+1, i-1)
			continue
		}
		if input[i] == '$' && (i == 0 || !identifierByte(input[i-1])) {
			tag := dollarTag.FindString(input[i:])
			if tag != "" {
				continuable = false
				start := i
				i += len(tag)
				end := strings.Index(input[i:], tag)
				if end < 0 {
					blank(start, len(input))
					return string(out), false
				}
				i += end + len(tag)
				blank(start, i)
				continue
			}
		}
		switch input[i] {
		case '\n', '\r':
			if continuable {
				newline = true
			}
		case ' ', '\t', '\f', '\v':
		default:
			continuable = false
		}
		i++
	}
	return string(out), true
}

func identifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= 128 || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
