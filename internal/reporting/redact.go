package reporting

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const redacted = "[REDACTED]"

var credentialURL = regexp.MustCompile(`(?i)\b((?:postgres(?:ql)?|https?)://)[^\s/@]+(?::[^\s/@]*)?@`)
var credentialAssignment = regexp.MustCompile(`(?i)((?:"?(?:password|passwd|pwd|token|secret|api[_-]?key)"?)\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}&\]]+)`)

// Redactor removes known ephemeral credentials and common credential-bearing
// connection-string formats. It cannot identify arbitrary sensitive fixture data.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	r.Add(secrets...)
	return r
}

func (r *Redactor) Add(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]bool, len(r.secrets))
	for _, secret := range r.secrets {
		seen[secret] = true
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, variant := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if !seen[variant] {
				r.secrets = append(r.secrets, variant)
				seen[variant] = true
			}
		}
	}
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

func (r *Redactor) String(value string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, redacted)
	}
	value = credentialURL.ReplaceAllString(value, "${1}"+redacted+"@")
	return credentialAssignment.ReplaceAllString(value, "${1}"+redacted)
}

func (r *Redactor) Clean(report Report) Report {
	data, err := json.Marshal(report)
	if err != nil {
		// Report fields contain only JSON values produced by HTTP decoding and
		// config validation. Preserve an explicit failure if a caller violates it.
		return Report{Version: 1, RunID: r.String(report.RunID), Config: r.String(report.Config), StartedAt: report.StartedAt, Status: "error", Error: "report contains values that cannot be serialized"}
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return report
	}
	var walk func(any) any
	walk = func(value any) any {
		switch current := value.(type) {
		case string:
			return r.String(current)
		case []any:
			for i, item := range current {
				current[i] = walk(item)
			}
		case map[string]any:
			for key, item := range current {
				current[key] = walk(item)
			}
		}
		return value
	}
	data, _ = json.Marshal(walk(value))
	var clean Report
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&clean) != nil {
		return report
	}
	return clean
}
