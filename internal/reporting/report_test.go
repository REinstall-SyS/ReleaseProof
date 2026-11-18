package reporting

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/REinstall-SyS/ReleaseProof/internal/assertions"
)

func evidence() Report {
	return Report{Version: 1, RunID: "run-test", Config: "unsafe.yaml", Fingerprint: "abc123", StartedAt: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), DurationMS: 12, Status: "incompatible", MigrationFiles: []string{"unsafe.sql"}, Images: map[string]string{"postgres": "sha256:123"}, Timings: map[string]int64{"migration": 2}, Risks: []Risk{{Code: "drop-column", File: "unsafe.sql", Message: "DROP COLUMN full_name"}}, Scenarios: []Scenario{{ID: "old-after", Name: "Old reads migrated schema", Kind: "old_after", Status: "fail", Error: "column users.full_name does not exist", Reproduce: `releaseproof reproduce old-after --report "local report.json"`, Versions: map[string]string{"old": "v1"}, MigrationMS: 2, Logs: map[string]string{"old": "database column error"}, Steps: []Step{{Name: "read", App: "old", Method: "GET", Path: "/users/7", Status: 500, Body: `<script>alert("fixture")</script>`, Error: "expected HTTP 200", Assertions: []assertions.Assertion{{Pointer: "HTTP status", Expected: 200, Actual: 500, Passed: false}}}}}}}
}

func TestWriteReadJSONAndHTMLEvidence(t *testing.T) {
	report := evidence()
	dir := t.TempDir()
	path, err := Write(dir, report)
	if err != nil {
		t.Fatal(err)
	}
	read, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Status != "incompatible" || read.Scenarios[0].Steps[0].Status != 500 || read.Scenarios[0].Logs["old"] != "database column error" {
		t.Fatalf("lost execution evidence: %+v", read)
	}
	html, err := os.ReadFile(filepath.Join(dir, "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"old-after", "Old reads migrated schema", "HTTP 500", "expected HTTP 200", "users.full_name", "database column error", "sha256:123", "&lt;script&gt;"} {
		if !bytes.Contains(html, []byte(text)) {
			t.Errorf("HTML missing executed evidence %q", text)
		}
	}
	if bytes.Contains(html, []byte(`<script>alert`)) {
		t.Fatal("untrusted response inserted as executable HTML")
	}
	if _, err := Write(dir, report); err != nil {
		t.Fatalf("cannot atomically replace report: %v", err)
	}
	entries, err := filepath.Glob(filepath.Join(dir, ".report-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary report files left behind: %v %v", entries, err)
	}
}

func TestTerminalShowsActualFailures(t *testing.T) {
	var output bytes.Buffer
	Terminal(&output, evidence())
	for _, text := range []string{"RESULT: INCOMPATIBLE", "HTTP 500", "expected 200; actual 500", "column users.full_name does not exist", "database column error", "releaseproof reproduce old-after"} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("terminal missing %q: %s", text, output.String())
		}
	}
}

func TestExitCodeContract(t *testing.T) {
	for status, expected := range map[string]int{"compatible": 0, "incompatible": 1, "baseline_failed": 3, "error": 3, "": 3, "unsupported": 3} {
		if actual := ExitCode(Report{Status: status}); actual != expected {
			t.Errorf("%s returned %d, want %d", status, actual, expected)
		}
	}
}

func TestReadRejectsInvalidOrMultipleDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	for _, text := range []string{`{}`, `{"version":2,"run_id":"x","config":"y"}`, `{"version":1,"run_id":"x","config":"y"} {}`, `not JSON`} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); err == nil {
			t.Errorf("accepted invalid report %s", text)
		}
	}
}

func TestRedactionRemovesCredentialsPreservesErrors(t *testing.T) {
	r := NewRedactor("random!secret+123")
	r.Add("ephemeral-credential")
	input := `dial postgres://rp:random%21secret%2B123@localhost/db failed; password='ephemeral-credential' token=another-secret SQLSTATE 42703: column users.full_name does not exist`
	output := r.String(input)
	for _, secret := range []string{"random", "ephemeral-credential", "another-secret"} {
		if strings.Contains(output, secret) {
			t.Errorf("credential not redacted: %s", output)
		}
	}
	if !strings.Contains(output, "SQLSTATE 42703: column users.full_name does not exist") || !strings.Contains(output, redacted) {
		t.Fatalf("lost diagnostic: %s", output)
	}
}

func TestReportRedactionCoversNestedResponseAndLogs(t *testing.T) {
	report := evidence()
	report.Scenarios[0].Steps[0].Body = `{"database_url":"postgresql://user:password123@db/proof"}`
	report.Scenarios[0].Logs["old"] = "credential is a-secret-test"
	report.Scenarios[0].Steps[0].Assertions[0].Actual = "a-secret-test"
	clean := NewRedactor("a-secret-test").Clean(report)
	data, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("password123")) || bytes.Contains(data, []byte("a-secret-test")) {
		t.Fatalf("nested secret leaked: %s", data)
	}
	if clean.Status != report.Status || clean.Scenarios[0].Steps[0].Status != 500 {
		t.Fatal("redaction changed result semantics")
	}
}

func TestWriteCreatesPrivateFiles(t *testing.T) {
	path, err := Write(filepath.Join(t.TempDir(), "nested"), evidence())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not implement Unix permissions. On platforms that do, report
	// content should remain private because it contains developer-provided data.
	if info.Mode().Perm()&0o077 != 0 && filepath.Separator != '\\' {
		t.Errorf("report has broad permissions: %v", info.Mode())
	}
}

func TestRedactionAndSerializationPreserveLargeNumericEvidence(t *testing.T) {
	report := evidence()
	report.Scenarios[0].Steps[0].Assertions[0].Actual = json.Number("9007199254740993")
	report.Scenarios[0].Steps[0].Assertions[0].Expected = int64(9007199254740992)
	clean := NewRedactor("private-secret").Clean(report)
	if clean.Scenarios[0].Steps[0].Assertions[0].Actual != json.Number("9007199254740993") {
		t.Fatalf("redaction rounded actual assertion value: %#v", clean.Scenarios[0].Steps[0].Assertions[0].Actual)
	}
	path, err := Write(t.TempDir(), clean)
	if err != nil {
		t.Fatal(err)
	}
	read, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	assertion := read.Scenarios[0].Steps[0].Assertions[0]
	if assertion.Actual != json.Number("9007199254740993") || assertion.Expected != json.Number("9007199254740992") {
		t.Fatalf("report rounded large numeric evidence: %+v", assertion)
	}
}
