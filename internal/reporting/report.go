// Package reporting writes the evidence produced by executed compatibility checks.
package reporting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/REinstall-SyS/ReleaseProof/internal/assertions"
)

type Report struct {
	Version        int               `json:"version"`
	RunID          string            `json:"run_id"`
	Config         string            `json:"config"`
	Fingerprint    string            `json:"fingerprint"`
	StartedAt      time.Time         `json:"started_at"`
	DurationMS     int64             `json:"duration_ms"`
	Status         string            `json:"status"`
	Error          string            `json:"error,omitempty"`
	MigrationFiles []string          `json:"migration_files"`
	Images         map[string]string `json:"images"`
	Scenarios      []Scenario        `json:"scenarios"`
	Risks          []Risk            `json:"risks,omitempty"`
	Timings        map[string]int64  `json:"timings"`
}

type Step = assertions.Result

type Scenario struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Status      string            `json:"status"`
	Error       string            `json:"error,omitempty"`
	Reproduce   string            `json:"reproduce"`
	DurationMS  int64             `json:"duration_ms"`
	Steps       []Step            `json:"steps"`
	Versions    map[string]string `json:"versions"`
	MigrationMS int64             `json:"migration_ms"`
	Logs        map[string]string `json:"logs,omitempty"`
}

type Risk struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	File    string `json:"file"`
}

// ExitCode is the public CLI result contract. Configuration failures are rejected
// before execution and use 2; they do not produce a compatibility verdict.
func ExitCode(report Report) int {
	switch report.Status {
	case "compatible":
		return 0
	case "incompatible":
		return 1
	default:
		return 3
	}
}

// Write publishes JSON and self-contained HTML. Each file is replaced atomically
// so interrupted writes cannot leave a partially serialized report.
func Write(dir string, report Report) (string, error) {
	report = NewRedactor().Clean(report)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create report directory: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("serialize report: %w", err)
	}
	var html bytes.Buffer
	if err := htmlReport.Execute(&html, report); err != nil {
		return "", fmt.Errorf("render report: %w", err)
	}
	jsonPath := filepath.Join(dir, "report.json")
	if err := atomicWrite(jsonPath, append(data, '\n')); err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(dir, "report.html"), html.Bytes()); err != nil {
		return "", err
	}
	return jsonPath, nil
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return fmt.Errorf("create report file: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write report: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync report: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close report: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("publish report: %w", err)
	}
	return nil
}

func Read(path string) (Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, fmt.Errorf("open run report: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("read run report: %w", err)
	}
	if report.Version != 1 || report.RunID == "" || report.Config == "" {
		return Report{}, fmt.Errorf("invalid or unsupported run report")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Report{}, fmt.Errorf("run report must contain one JSON document")
	}
	return report, nil
}

func Terminal(writer io.Writer, report Report) {
	report = NewRedactor().Clean(report)
	fmt.Fprintln(writer, "RELEASEPROOF\nPre-deployment database compatibility")
	fmt.Fprintf(writer, "\nMigration: %s\n\n", strings.Join(report.MigrationFiles, ", "))
	for _, scenario := range report.Scenarios {
		fmt.Fprintf(writer, "[%s] %s (%s, %d ms)\n", strings.ToUpper(scenario.Status), scenario.Name, scenario.ID, scenario.DurationMS)
		if scenario.Error != "" {
			fmt.Fprintf(writer, "  %s\n", strings.ReplaceAll(scenario.Error, "\n", "\n  "))
		}
		for _, step := range scenario.Steps {
			if step.Error == "" {
				continue
			}
			fmt.Fprintf(writer, "  %s: %s %s → HTTP %d\n", step.App, step.Method, step.Path, step.Status)
			for _, assertion := range step.Assertions {
				if !assertion.Passed {
					fmt.Fprintf(writer, "    %s: expected %s; actual %s", assertion.Pointer, jsonText(assertion.Expected), jsonText(assertion.Actual))
					if assertion.Error != "" {
						fmt.Fprintf(writer, " (%s)", assertion.Error)
					}
					fmt.Fprintln(writer)
				}
			}
			if step.Body != "" {
				fmt.Fprintf(writer, "    Response: %s\n", strings.ReplaceAll(step.Body, "\n", "\n    "))
			}
		}
		if scenario.Status != "pass" {
			for name, logs := range scenario.Logs {
				if logs != "" {
					fmt.Fprintf(writer, "  %s logs:\n    %s\n", name, strings.ReplaceAll(logs, "\n", "\n    "))
				}
			}
		}
		if scenario.Status == "fail" || scenario.Status == "error" {
			if scenario.Reproduce != "" {
				fmt.Fprintf(writer, "  Reproduce: %s\n", scenario.Reproduce)
			}
		}
	}
	for _, risk := range report.Risks {
		fmt.Fprintf(writer, "[RISK %s] %s: %s\n", risk.Code, risk.File, risk.Message)
	}
	if report.Error != "" {
		fmt.Fprintf(writer, "\nExecution error: %s\n", report.Error)
	}
	fmt.Fprintf(writer, "\nRESULT: %s (%d ms)\n", strings.ToUpper(report.Status), report.DurationMS)
}

func jsonText(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

var htmlReport = template.Must(template.New("report").Funcs(template.FuncMap{
	"upper": strings.ToUpper,
	"json":  jsonText,
	"join":  strings.Join,
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'"><title>ReleaseProof — {{upper .Status}}</title>
<style>body{font:16px/1.55 system-ui,sans-serif;background:#f5f7fa;color:#182334;max-width:1100px;margin:auto;padding:32px}h1{font-size:34px;margin:0}h2{font-size:22px}h3{margin-bottom:8px}p{margin:8px 0}article{background:white;border:1px solid #d5dde8;border-radius:8px;padding:20px;margin:18px 0}.status{font-weight:700}.pass,.compatible{color:#14663a}.fail,.incompatible,.error,.baseline_failed{color:#a21e29}.skipped,.unsupported{color:#725a10}table{border-collapse:collapse;width:100%;margin:10px 0}th,td{text-align:left;padding:8px;border-bottom:1px solid #dde3eb;vertical-align:top}pre,code{font:13px/1.5 ui-monospace,Consolas,monospace}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#eef2f6;padding:12px;border-radius:4px}small{color:#526173}summary{cursor:pointer}dl{display:grid;grid-template-columns:150px 1fr;gap:6px}dt{font-weight:600}dd{margin:0;overflow-wrap:anywhere}</style></head>
<body><h1>ReleaseProof</h1><p>Catch database migrations that break running applications before deployment.</p><h2 class="status {{.Status}}">{{upper .Status}}</h2>
<dl><dt>Run</dt><dd>{{.RunID}}</dd><dt>Started</dt><dd>{{.StartedAt}}</dd><dt>Duration</dt><dd>{{.DurationMS}} ms</dd><dt>Configuration</dt><dd>{{.Config}}</dd><dt>Migrations</dt><dd>{{join .MigrationFiles ", "}}</dd><dt>Fingerprint</dt><dd><code>{{.Fingerprint}}</code></dd></dl>
{{if .Error}}<article><h2>Execution error</h2><pre>{{.Error}}</pre></article>{{end}}
{{range .Scenarios}}<article><h2><span class="status {{.Status}}">{{upper .Status}}</span> · {{.Name}}</h2><p><code>{{.ID}}</code> · {{.Kind}} · {{.DurationMS}} ms · migration {{.MigrationMS}} ms</p><p>{{range $app,$version := .Versions}}<strong>{{$app}}</strong> {{$version}} · {{end}}</p>
{{if .Error}}<pre>{{.Error}}</pre>{{end}}
{{range .Steps}}<h3>{{.Name}}</h3><p><strong>{{.App}}</strong> · <code>{{.Method}} {{.Path}}</code> → HTTP {{.Status}} · {{.DurationMS}} ms</p>
{{if .Error}}<pre>{{.Error}}</pre>{{end}}
{{if .Assertions}}<table><thead><tr><th>Assertion</th><th>Expected</th><th>Actual</th><th>Result</th></tr></thead><tbody>{{range .Assertions}}<tr><td><code>{{.Pointer}}</code></td><td><code>{{json .Expected}}</code></td><td><code>{{json .Actual}}</code></td><td>{{if .Passed}}PASS{{else}}FAIL{{if .Error}}: {{.Error}}{{end}}{{end}}</td></tr>{{end}}</tbody></table>{{end}}
{{if .Body}}<details{{if .Error}} open{{end}}><summary>Actual response</summary><pre>{{.Body}}</pre></details>{{end}}{{end}}
{{if ne .Status "pass"}}{{range $name,$logs := .Logs}}{{if $logs}}<details><summary>{{$name}} logs</summary><pre>{{$logs}}</pre></details>{{end}}{{end}}{{end}}
{{if .Reproduce}}<h3>Reproduction</h3><pre>{{.Reproduce}}</pre>{{end}}</article>{{end}}
{{if .Risks}}<article><h2>Migration risks</h2><p>Heuristic SQL warnings; executable scenario results determine the verdict.</p>{{range .Risks}}<p><strong>{{.Code}}</strong> · {{.File}}</p><pre>{{.Message}}</pre>{{end}}</article>{{end}}
<article><h2>Resolved images</h2><dl>{{range $name,$image := .Images}}<dt>{{$name}}</dt><dd><code>{{$image}}</code></dd>{{end}}</dl><h2>Phase timings</h2><dl>{{range $name,$ms := .Timings}}<dt>{{$name}}</dt><dd>{{$ms}} ms</dd>{{end}}</dl></article>
<small>Evidence is limited to the supplied fixtures, assertions, and operation sequences. Reports may include application response data.</small></body></html>`))
