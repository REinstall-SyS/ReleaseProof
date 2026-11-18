// Package analysis provides conservative SQL text warnings. It is intentionally
// not a PostgreSQL parser or a replacement for the executable compatibility matrix.
package analysis

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/REinstall-SyS/ReleaseProof/internal/config"
	"github.com/REinstall-SyS/ReleaseProof/internal/reporting"
)

type rule struct {
	code, message string
	pattern       *regexp.Regexp
}

var rules = []rule{
	{"drop-column", "DROP COLUMN removes an application-visible field. Confirm all supported versions have retired that field.", regexp.MustCompile(`(?i)\bDROP\s+COLUMN\b`)},
	{"drop-table", "DROP TABLE removes an application-visible relation. Confirm no supported version still uses it.", regexp.MustCompile(`(?i)\bDROP\s+TABLE\b`)},
	{"rename-column", "RENAME COLUMN changes an application-visible field name. Confirm all supported versions can use the new name.", regexp.MustCompile(`(?is)\bALTER\s+TABLE\b[^;]*?\bRENAME\s+(?:COLUMN\s+)?(?:"(?:[^"]|"")*"|[a-z_][a-z_0-9]*)\s+TO\b`)},
	{"alter-type", "ALTER COLUMN TYPE changes a stored representation and may rewrite the table. Review application decoding and lock behavior.", regexp.MustCompile(`(?is)\bALTER\s+(?:COLUMN\s+)?(?:"(?:[^"]|"")*"|[a-z_][a-z_0-9]*)\s+(?:SET\s+DATA\s+)?TYPE\b`)},
	{"not-null", "SET NOT NULL can reject writes from older versions that omit this value. Review defaults, backfill, and application write paths.", regexp.MustCompile(`(?i)\bSET\s+NOT\s+NULL\b`)},
	{"added-not-null", "An added definition contains NOT NULL. Review whether older applications supply or default the required value.", regexp.MustCompile(`(?is)\bADD\s+[^;,]*?\bNOT\s+NULL\b`)},
	{"alter-table-lock", "ALTER TABLE may acquire strong locks. Lock mode and duration depend on the subcommand and workload; this warning does not measure blocking.", regexp.MustCompile(`(?i)\bALTER\s+TABLE\b`)},
}

// Scan returns one warning per rule per file. Comments, string literals and
// dollar-quoted bodies are masked to avoid treating examples or function text as
// top-level DDL. Dynamic SQL and dependency effects may therefore be missed.
func Scan(c *config.Config) []reporting.Risk {
	var result []reporting.Risk
	for _, file := range c.Migrations {
		data, err := os.ReadFile(c.Resolve(file))
		if err != nil {
			result = append(result, reporting.Risk{Code: "analysis-unavailable", File: file, Message: fmt.Sprintf("Could not read migration for heuristic analysis: %v", err)})
			continue
		}
		sql, complete := mask(string(data))
		if !complete {
			result = append(result, reporting.Risk{Code: "analysis-incomplete", File: file, Message: "Heuristic analysis encountered an unterminated quoted literal or comment. PostgreSQL execution determines SQL validity."})
		}
		for _, r := range rules {
			if r.pattern.MatchString(sql) {
				result = append(result, reporting.Risk{Code: r.code, File: file, Message: "SQL text heuristic: " + r.message})
			}
		}
		for _, loc := range indexDDL.FindAllStringIndex(sql, -1) {
			rest := strings.TrimSpace(sql[loc[1]:])
			if !concurrently.MatchString(rest) {
				result = append(result, reporting.Risk{Code: "index-write-lock", File: file, Message: "SQL text heuristic: CREATE INDEX without CONCURRENTLY can block table writes during index construction. No workload or lock duration was measured."})
				break
			}
		}
	}
	return result
}

var indexDDL = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\b`)
var concurrently = regexp.MustCompile(`(?i)^CONCURRENTLY\b`)
var dollarTag = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)

// mask preserves byte positions/newlines and quoted identifier boundaries. Escape strings
// are handled conservatively as allowing backslash escapes; SQL execution is the
// authority on syntax. PostgreSQL nested block comments are supported.
func mask(input string) (string, bool) {
	out := []byte(input)
	blank := func(start, end int) {
		for j := start; j < end; j++ {
			if out[j] != '\n' && out[j] != '\r' {
				out[j] = ' '
			}
		}
	}
	for i := 0; i < len(input); {
		if strings.HasPrefix(input[i:], "--") {
			end := strings.IndexByte(input[i:], '\n')
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
			start := i
			depth := 1
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
			blank(start, i)
			if depth != 0 {
				return string(out), false
			}
			continue
		}
		if input[i] == '\'' {
			start := i
			i++
			closed := false
			for i < len(input) {
				if input[i] == '\\' && i+1 < len(input) {
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
			continue
		}
		if input[i] == '"' {
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
		if input[i] == '$' {
			tag := dollarTag.FindString(input[i:])
			if tag != "" {
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
		i++
	}
	return string(out), true
}
