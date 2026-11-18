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

func TestTerminalLeadsWithFullMatrixAndBoundsDiagnosticsOnly(t *testing.T) {
	report := evidence()
	report.Scenarios = append(report.Scenarios, Scenario{ID: "new-after", Name: "New reads migrated schema", Kind: "new_after", Status: "pass"})
	report.Scenarios[0].Steps[0].Body = strings.Repeat("x", 1200) + "UNTRUNCATED_RESPONSE_END"
	report.Scenarios[0].Logs = map[string]string{"z-app": "boot-old\n\nline-2\nline-3\nline-4\nline-5\nline-6\n", "a-postgres": "database column error"}
	report.Error = strings.Repeat("e", 2200) + "UNTRUNCATED_ERROR_END"
	var output bytes.Buffer
	Terminal(&output, report)
	text := output.String()
	matrixLast := strings.Index(text, "[PASS] New reads migrated schema")
	verdict := strings.Index(text, "RESULT: INCOMPATIBLE")
	details := strings.Index(text, "Scenario: Old reads migrated schema")
	if matrixLast < 0 || verdict <= matrixLast || details <= verdict {
		t.Fatalf("full matrix and verdict must precede diagnostics: %s", text)
	}
	for _, expected := range []string{"Versions: old=v1", "HTTP 500", "database column error", "releaseproof reproduce old-after", "[truncated; full evidence in JSON/HTML report]", "full logs in JSON/HTML report", "line-3", "line-6"} {
		if !strings.Contains(text, expected) {
			t.Errorf("terminal lost required concise evidence %q: %s", expected, text)
		}
	}
	for _, omitted := range []string{"UNTRUNCATED_RESPONSE_END", "UNTRUNCATED_ERROR_END", "boot-old", "line-2"} {
		if strings.Contains(text, omitted) {
			t.Errorf("terminal diagnostic was not bounded: %q", omitted)
		}
	}
	if strings.Index(text, "a-postgres logs") > strings.Index(text, "z-app logs") {
		t.Error("terminal log order is nondeterministic")
	}
	path, err := Write(t.TempDir(), report)
	if err != nil {
		t.Fatal(err)
	}
	read, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Scenarios[0].Steps[0].Body != report.Scenarios[0].Steps[0].Body || read.Scenarios[0].Logs["z-app"] != report.Scenarios[0].Logs["z-app"] || read.Error != report.Error {
		t.Fatal("terminal clipping reduced full JSON evidence")
	}
	html, err := os.ReadFile(filepath.Join(filepath.Dir(path), "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fullEvidence := range []string{"UNTRUNCATED_RESPONSE_END", "UNTRUNCATED_ERROR_END", "boot-old"} {
		if !bytes.Contains(html, []byte(fullEvidence)) {
			t.Errorf("terminal clipping reduced HTML evidence: missing %s", fullEvidence)
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

func TestShortSecretsDoNotCorruptResultEnumsOrIdentities(t *testing.T) {
	report := evidence()
	report.RunID = "run-pass-old"
	report.Fingerprint = "pass-old-content-hash"
	report.Images["old"] = "sha256:pass-old-image"
	report.Status = "compatible"
	report.Error = "credential pass from old application"
	report.Scenarios[0].ID = "old-pass"
	report.Scenarios[0].Kind = "old_after"
	report.Scenarios[0].Status = "pass"
	report.Scenarios[0].Error = "pass old GET"
	report.Scenarios[0].Steps[0].App = "old"
	report.Scenarios[0].Steps[0].Method = "GET"
	report.Scenarios[0].Steps[0].Body = "password is pass; version old; verb GET"
	report.Scenarios[0].Logs["old"] = "credential pass"
	clean := NewRedactor("pass", "old", "GET").Clean(report)
	if clean.Status != "compatible" || clean.Scenarios[0].Status != "pass" || clean.Scenarios[0].Kind != "old_after" || clean.Scenarios[0].Steps[0].App != "old" || clean.Scenarios[0].Steps[0].Method != "GET" {
		t.Fatalf("short credential changed result semantics: %+v", clean)
	}
	if clean.RunID != report.RunID || clean.Fingerprint != report.Fingerprint || clean.Images["old"] != report.Images["old"] || clean.Scenarios[0].ID != report.Scenarios[0].ID {
		t.Fatalf("redaction changed stable metadata needed for reproduction: %+v", clean)
	}
	if _, present := clean.Scenarios[0].Versions["old"]; !present {
		t.Fatal("redaction changed schema map keys")
	}
	for _, text := range []string{clean.Error, clean.Scenarios[0].Error, clean.Scenarios[0].Steps[0].Body, clean.Scenarios[0].Logs["old"]} {
		if strings.Contains(text, "pass") || strings.Contains(text, "old") || strings.Contains(text, "GET") {
			t.Errorf("short credential remains in free-text evidence: %s", text)
		}
	}
	if reportingExit := ExitCode(clean); reportingExit != 0 {
		t.Fatalf("redaction changed CLI result: %d", reportingExit)
	}
}

func TestShortSecretsPreserveLocalPathsNeededForReproduction(t *testing.T) {
	report := evidence()
	report.Config = "C:/work/compass/releaseproof.yaml"
	report.MigrationFiles = []string{"sql/password_expansion.sql"}
	report.Scenarios[0].Reproduce = `releaseproof reproduce old-after --report "C:/work/compass/.releaseproof/run/report.json"`
	report.Scenarios[0].Error = "password was pass"
	report.Scenarios[0].Steps[0].Body = `{"credential":"pass"}`
	clean := NewRedactor("pass").Clean(report)
	if clean.Config != report.Config || len(clean.MigrationFiles) != 1 || clean.MigrationFiles[0] != report.MigrationFiles[0] || clean.Scenarios[0].Reproduce != report.Scenarios[0].Reproduce {
		t.Fatalf("redaction corrupted local reproduction paths: %+v", clean)
	}
	if strings.Contains(clean.Scenarios[0].Error, "pass") || strings.Contains(clean.Scenarios[0].Steps[0].Body, "pass") {
		t.Fatal("preserving local metadata disabled credential redaction in diagnostics")
	}
	path, err := Write(t.TempDir(), clean)
	if err != nil {
		t.Fatal(err)
	}
	read, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Config != report.Config || read.MigrationFiles[0] != report.MigrationFiles[0] || read.Scenarios[0].Reproduce != report.Scenarios[0].Reproduce {
		t.Fatal("serialized metadata cannot locate the original configuration/report")
	}
}
