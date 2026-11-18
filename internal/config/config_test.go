package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func fixture(t *testing.T) *Config {
	t.Helper()
	root := t.TempDir()
	for name, sql := range map[string]string{"baseline.sql": "CREATE TABLE users (id integer, full_name text);", "seed.sql": "INSERT INTO users VALUES (7, 'Ada Lovelace');", "migration.sql": "ALTER TABLE users ADD COLUMN first_name text;"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(sql), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(app string) Step {
		return Step{Name: "read", App: app, Method: "GET", Path: "/users/7", Expect: Expect{Status: 200, JSON: map[string]any{"/id": 7}}}
	}
	write := func(app string) Step {
		return Step{Name: "write", App: app, Method: "POST", Path: "/users", Body: map[string]any{"name": "Grace Hopper"}, Expect: Expect{Status: 201}}
	}
	return &Config{
		Version: 1, Path: filepath.Join(root, "releaseproof.yaml"), Postgres: Postgres{Image: "postgres:17.12-alpine"}, Baseline: "baseline.sql", Seed: "seed.sql", Migrations: []string{"migration.sql"},
		Timeout: "2m", RequestTimeout: "5s", StartupTimeout: "30s", Report: ".releaseproof",
		Apps: map[string]App{"old": {Version: "v1", Image: "demo-old:test", Port: 8080, Health: "/health"}, "new": {Version: "v2", Image: "demo-new:test", Port: 8080, Health: "/health"}},
		Scenarios: []Scenario{
			{ID: "baseline", Name: "old baseline", Kind: "baseline", Steps: []Step{read("old")}},
			{ID: "old-after", Name: "old migrated", Kind: "old_after", Steps: []Step{read("old")}},
			{ID: "new-after", Name: "new migrated", Kind: "new_after", Steps: []Step{read("new")}},
			{ID: "old-to-new", Name: "old writes new reads", Kind: "old_to_new", Steps: []Step{write("old"), read("new")}},
			{ID: "new-to-old", Name: "new writes old reads", Kind: "new_to_old", Steps: []Step{write("new"), read("old")}},
		},
	}
}

func writeConfig(t *testing.T, c *Config) string {
	t.Helper()
	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return c.Path
}

func TestLoadDefaultsAndRelativeInputs(t *testing.T) {
	c := fixture(t)
	c.Postgres.Image = ""
	c.Timeout = ""
	c.RequestTimeout = ""
	c.StartupTimeout = ""
	c.Report = ""
	app := c.Apps["old"]
	app.Health = ""
	c.Apps["old"] = app
	path := writeConfig(t, c)
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Postgres.Image != "postgres:17.12-alpine" || got.Report != ".releaseproof" || got.Apps["old"].Health != "/health" {
		t.Fatalf("defaults not populated: %+v", got)
	}
	if got.Duration() != time.Minute || got.RequestDuration() != 5*time.Second || got.StartupDuration() != 30*time.Second || got.Apps["old"].StartupDuration() != 30*time.Second {
		t.Fatalf("wrong duration defaults: %+v", got)
	}
	if !filepath.IsAbs(got.Path) || got.Resolve(got.Baseline) != filepath.Join(filepath.Dir(path), "baseline.sql") {
		t.Fatal("inputs must resolve relative to configuration")
	}
}

func TestStrictYAML(t *testing.T) {
	for _, tc := range []struct{ name, extra, want string }{
		{"unknown field", "unexpected: true\n", "field unexpected"},
		{"duplicate key", "version: 1\n", "already defined"},
		{"extra document", "---\nversion: 1\n", "exactly one YAML document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			path := writeConfig(t, c)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.WriteString(tc.extra); err != nil {
				t.Fatal(err)
			}
			f.Close()
			_, err = Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %q", err, tc.want)
			}
		})
	}
}

func TestValidationRejectsInvalidContracts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"version", func(c *Config) { c.Version = 2 }, "version"},
		{"missing baseline", func(c *Config) { c.Baseline = "missing.sql" }, "missing.sql"},
		{"missing migration", func(c *Config) { c.Migrations = nil }, "at least one migration"},
		{"directory input", func(c *Config) { c.Baseline = "." }, "regular file"},
		{"invalid timeout", func(c *Config) { c.Timeout = "forever" }, "timeout"},
		{"request timeout", func(c *Config) { c.RequestTimeout = "0s" }, "request_timeout"},
		{"startup timeout", func(c *Config) { c.StartupTimeout = "31m" }, "startup_timeout"},
		{"missing old", func(c *Config) { delete(c.Apps, "old") }, "exactly old and new"},
		{"extra app", func(c *Config) { c.Apps["third"] = c.Apps["old"] }, "exactly old and new"},
		{"image and build", func(c *Config) { a := c.Apps["old"]; a.Build = "."; c.Apps["old"] = a }, "exactly one"},
		{"build missing", func(c *Config) { a := c.Apps["old"]; a.Image = ""; a.Build = "absent"; c.Apps["old"] = a }, "build context"},
		{"image option injection", func(c *Config) { a := c.Apps["old"]; a.Image = "--privileged"; c.Apps["old"] = a }, "cannot begin"},
		{"invalid port", func(c *Config) { a := c.Apps["old"]; a.Port = 65536; c.Apps["old"] = a }, "port"},
		{"health host", func(c *Config) { a := c.Apps["old"]; a.Health = "//example.com"; c.Apps["old"] = a }, "local HTTP path"},
		{"managed env", func(c *Config) {
			a := c.Apps["old"]
			a.Env = map[string]string{"DATABASE_URL": "external"}
			c.Apps["old"] = a
		}, "managed by ReleaseProof"},
		{"invalid env name", func(c *Config) { a := c.Apps["old"]; a.Env = map[string]string{"BAD=NAME": "x"}; c.Apps["old"] = a }, "portable names"},
		{"NUL command", func(c *Config) { a := c.Apps["old"]; a.Command = []string{"bad\x00arg"}; c.Apps["old"] = a }, "command arguments"},
		{"duplicate id", func(c *Config) { c.Scenarios[1].ID = c.Scenarios[0].ID }, "unique"},
		{"invalid id", func(c *Config) { c.Scenarios[0].ID = "Baseline" }, "lowercase"},
		{"unknown kind", func(c *Config) { c.Scenarios[0].Kind = "custom" }, "unknown scenario kind"},
		{"missing baseline scenario", func(c *Config) { c.Scenarios = c.Scenarios[1:] }, "missing required scenario kind: baseline"},
		{"empty steps", func(c *Config) { c.Scenarios[0].Steps = nil }, "requires a name and steps"},
		{"bad HTTP method", func(c *Config) { c.Scenarios[0].Steps[0].Method = "TRACE" }, "unsupported HTTP method"},
		{"external URL", func(c *Config) { c.Scenarios[0].Steps[0].Path = "https://example.com" }, "local HTTP path"},
		{"missing status", func(c *Config) { c.Scenarios[0].Steps[0].Expect.Status = 0 }, "expect.status"},
		{"invalid pointer", func(c *Config) { c.Scenarios[0].Steps[0].Expect.JSON = map[string]any{"/bad~2": 1} }, "JSON pointers"},
		{"wrong version for kind", func(c *Config) { c.Scenarios[0].Steps[0].App = "new" }, "requires only old"},
		{"wrong write direction", func(c *Config) { c.Scenarios[3].Steps[0].App = "new" }, "writer mutation"},
		{"missing semantic read", func(c *Config) { c.Scenarios[3].Steps[1].Expect.JSON = nil }, "JSON assertions"},
		{"read before write", func(c *Config) { s := c.Scenarios[3].Steps; s[0], s[1] = s[1], s[0] }, "writer mutation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			tc.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %q", err, tc.want)
			}
		})
	}
}

func TestResponseVariableOrdering(t *testing.T) {
	c := fixture(t)
	steps := c.Scenarios[3].Steps
	steps[0].Capture = map[string]string{"created_id": "/id"}
	steps[1].Path = "/users/{{created_id}}"
	steps[1].Expect.JSON["/id"] = "{{created_id}}"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	steps[0].Body = map[string]any{"id": "{{created_id}}"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "undefined response variable") {
		t.Fatalf("same-step capture must not resolve: %v", err)
	}
}

func TestVariablesInNestedJSONAndMalformedTokens(t *testing.T) {
	for _, value := range []any{"{{missing}}", []any{map[string]any{"id": "{{missing}}"}}, "{{Bad}}", "{{ missing }}", map[any]any{1: "non-string"}} {
		if err := validateVariables(value, map[string]bool{}); err == nil {
			t.Fatalf("accepted invalid value %#v", value)
		}
	}
	if err := validateVariables(map[string]any{"nested": []any{"id={{row_id}}", nil, 3, true}}, map[string]bool{"row_id": true}); err != nil {
		t.Fatal(err)
	}
}

func TestPointerGrammar(t *testing.T) {
	for _, value := range []string{"", "/", "/users/0/name", "/a~1b/~0"} {
		if !pointer(value) {
			t.Fatalf("valid pointer rejected: %q", value)
		}
	}
	for _, value := range []string{"name", "/~", "/~3", "/a~0b~"} {
		if pointer(value) {
			t.Fatalf("invalid pointer accepted: %q", value)
		}
	}
}

func TestFingerprintIncludesSQLAndBuildInputs(t *testing.T) {
	c := fixture(t)
	original, err := c.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := c.Fingerprint()
	if err != nil || original != repeat {
		t.Fatal("fingerprint is not stable")
	}
	if err := os.WriteFile(c.Resolve("migration.sql"), []byte("ALTER TABLE users DROP COLUMN full_name;"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := c.Fingerprint()
	if err != nil || changed == original {
		t.Fatal("SQL modification did not change fingerprint")
	}
	app := c.Apps["old"]
	app.Image = ""
	app.Build = "."
	c.Apps["old"] = app
	if err := os.WriteFile(c.Resolve("app.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := c.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Resolve("app.go"), []byte("package main\nfunc main(){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := c.Fingerprint()
	if err != nil || before == after {
		t.Fatal("build input modification did not change fingerprint")
	}
	for _, name := range []string{".git", ".tools", ".releaseproof", "bin"} {
		if err := os.MkdirAll(c.Resolve(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.Resolve(filepath.Join(name, "artifact")), []byte("runtime output"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"unit-tests.txt", "integration-tests.txt"} {
		if err := os.WriteFile(c.Resolve(name), []byte("CI execution log"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ignored, err := c.Fingerprint()
	if err != nil || ignored != after {
		t.Fatalf("runtime artifacts changed fingerprint: %v", err)
	}
}

func TestFingerprintIncludesConfiguration(t *testing.T) {
	c := fixture(t)
	before, err := c.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	c.Scenarios[0].Steps[0].Expect.JSON["/id"] = 8
	after, err := c.Fingerprint()
	if err != nil || before == after {
		t.Fatal("assertion change did not change fingerprint")
	}
}

func TestFingerprintExcludesCustomReportsInsideBuildContext(t *testing.T) {
	c := fixture(t)
	app := c.Apps["old"]
	app.Image = ""
	app.Build = "."
	c.Apps["old"] = app
	c.Report = "artifacts/results"
	before, err := c.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Resolve(c.Report), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Resolve(filepath.Join(c.Report, "report.json")), []byte(`{"status":"compatible"}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := c.Fingerprint()
	if err != nil || before != after {
		t.Fatalf("configured report output changed fingerprint: %v", err)
	}
}

func TestCommandAndEnvironmentRoundTrip(t *testing.T) {
	c := fixture(t)
	a := c.Apps["old"]
	a.Command = []string{"/app/server", "--literal=$(echo no-shell)"}
	a.Env = map[string]string{"MODE": "test"}
	c.Apps["old"] = a
	loaded, err := Load(writeConfig(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Apps["old"].Command[1] != a.Command[1] || loaded.Apps["old"].Env["MODE"] != "test" {
		t.Fatal("configuration changed literal process arguments")
	}
}
