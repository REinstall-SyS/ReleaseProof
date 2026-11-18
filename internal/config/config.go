// Package config loads strict, versioned ReleaseProof configuration.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Version        int            `yaml:"version" json:"version"`
	Postgres       Postgres       `yaml:"postgres" json:"postgres"`
	Baseline       string         `yaml:"baseline" json:"baseline"`
	Seed           string         `yaml:"seed" json:"seed"`
	Migrations     []string       `yaml:"migrations" json:"migrations"`
	Apps           map[string]App `yaml:"apps" json:"apps"`
	Scenarios      []Scenario     `yaml:"scenarios" json:"scenarios"`
	Timeout        string         `yaml:"timeout" json:"timeout"`
	RequestTimeout string         `yaml:"request_timeout" json:"request_timeout"`
	StartupTimeout string         `yaml:"startup_timeout" json:"startup_timeout"`
	Report         string         `yaml:"report" json:"report"`
	Path           string         `yaml:"-" json:"-"`
}
type Postgres struct {
	Image string `yaml:"image" json:"image"`
}
type App struct {
	Version        string            `yaml:"version" json:"version"`
	Image          string            `yaml:"image" json:"image,omitempty"`
	Build          string            `yaml:"build" json:"build,omitempty"`
	Dockerfile     string            `yaml:"dockerfile" json:"dockerfile,omitempty"`
	Port           int               `yaml:"port" json:"port"`
	Health         string            `yaml:"health" json:"health"`
	Env            map[string]string `yaml:"env" json:"env,omitempty"`
	Command        []string          `yaml:"command" json:"command,omitempty"`
	StartupTimeout string            `yaml:"startup_timeout" json:"startup_timeout,omitempty"`
}
type Scenario struct {
	ID    string `yaml:"id" json:"id"`
	Name  string `yaml:"name" json:"name"`
	Kind  string `yaml:"kind" json:"kind"`
	Steps []Step `yaml:"steps" json:"steps"`
}
type Step struct {
	Name    string            `yaml:"name" json:"name"`
	App     string            `yaml:"app" json:"app"`
	Method  string            `yaml:"method" json:"method"`
	Path    string            `yaml:"path" json:"path"`
	Body    any               `yaml:"body" json:"body,omitempty"`
	Expect  Expect            `yaml:"expect" json:"expect"`
	Capture map[string]string `yaml:"capture" json:"capture,omitempty"`
}
type Expect struct {
	Status int            `yaml:"status" json:"status"`
	JSON   map[string]any `yaml:"json" json:"json,omitempty"`
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var environmentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var variable = regexp.MustCompile(`\{\{([a-z][a-z0-9_-]{0,62})\}\}`)
var RequiredKinds = []string{"baseline", "old_after", "new_after", "old_to_new", "new_to_old"}

func Load(path string) (*Config, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err = dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("configuration must contain exactly one YAML document")
	}
	c.Path = absolute
	if c.Postgres.Image == "" {
		c.Postgres.Image = "postgres:17.12-alpine"
	}
	if c.Timeout == "" {
		c.Timeout = "60s"
	}
	if c.RequestTimeout == "" {
		c.RequestTimeout = "5s"
	}
	if c.StartupTimeout == "" {
		c.StartupTimeout = "30s"
	}
	if c.Report == "" {
		c.Report = ".releaseproof"
	}
	for key, app := range c.Apps {
		if app.Health == "" {
			app.Health = "/health"
		}
		if app.StartupTimeout == "" {
			app.StartupTimeout = c.StartupTimeout
		}
		c.Apps[key] = app
	}
	if err = c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}
func (c *Config) Resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(filepath.Dir(c.Path), path)
}
func (c *Config) Duration() time.Duration { d, _ := time.ParseDuration(c.Timeout); return d }
func (c *Config) RequestDuration() time.Duration {
	d, _ := time.ParseDuration(c.RequestTimeout)
	return d
}
func (c *Config) StartupDuration() time.Duration {
	d, _ := time.ParseDuration(c.StartupTimeout)
	return d
}
func (a App) StartupDuration() time.Duration { d, _ := time.ParseDuration(a.StartupTimeout); return d }
func (c *Config) Validate() error {
	fail := func(s string) error { return fmt.Errorf("configuration: %s", s) }
	if c.Version != 1 {
		return fail("version must be 1")
	}
	if c.Baseline == "" || len(c.Migrations) == 0 {
		return fail("baseline and at least one migration are required")
	}
	if c.Postgres.Image == "" || strings.HasPrefix(c.Postgres.Image, "-") {
		return fail("postgres.image must be an image reference")
	}
	if d, err := time.ParseDuration(c.Timeout); err != nil || d < time.Second || d > 30*time.Minute {
		return fail("timeout must be between 1s and 30m")
	}
	if d, err := time.ParseDuration(c.RequestTimeout); err != nil || d < time.Millisecond || d > 30*time.Minute {
		return fail("request_timeout must be between 1ms and 30m")
	}
	if d, err := time.ParseDuration(c.StartupTimeout); err != nil || d < time.Millisecond || d > 30*time.Minute {
		return fail("startup_timeout must be between 1ms and 30m")
	}
	if strings.ContainsRune(c.Report, '\x00') {
		return fail("report cannot contain NUL")
	}
	if len(c.Apps) != 2 {
		return fail("apps must define exactly old and new")
	}
	for _, name := range []string{"old", "new"} {
		app, ok := c.Apps[name]
		if !ok {
			return fail("apps must define old and new")
		}
		if app.Version == "" || app.Port < 1 || app.Port > 65535 {
			return fail("each app requires version and port (1..65535)")
		}
		if (app.Image == "") == (app.Build == "") {
			return fail("each app requires exactly one of image or build")
		}
		if strings.HasPrefix(app.Image, "-") {
			return fail("app image cannot begin with '-'")
		}
		if app.StartupTimeout != "" {
			if d, err := time.ParseDuration(app.StartupTimeout); err != nil || d < time.Millisecond || d > 30*time.Minute {
				return fail("app startup_timeout must be between 1ms and 30m")
			}
		}
		if !localPath(app.Health) || strings.Contains(app.Health, "{{") {
			return fail("health must be an absolute local HTTP path")
		}
		for key, value := range app.Env {
			if key == "DATABASE_URL" || key == "PORT" {
				return fail("DATABASE_URL and PORT are managed by ReleaseProof")
			}
			if !environmentKey.MatchString(key) || strings.ContainsRune(value, '\x00') {
				return fail("environment keys must be portable names and values cannot contain NUL")
			}
		}
		for _, arg := range app.Command {
			if strings.ContainsRune(arg, '\x00') {
				return fail("command arguments cannot contain NUL")
			}
		}
		if len(app.Command) > 0 && app.Command[0] == "" {
			return fail("command executable cannot be empty")
		}
		if app.Build != "" {
			info, err := os.Stat(c.Resolve(app.Build))
			if err != nil || !info.IsDir() {
				return fail("app build context is not a directory: " + app.Build)
			}
			if app.Dockerfile != "" {
				if err := file(c.Resolve(filepath.Join(app.Build, app.Dockerfile))); err != nil {
					return err
				}
			}
		}
	}
	for _, path := range append([]string{c.Baseline, c.Seed}, c.Migrations...) {
		if path != "" {
			if err := file(c.Resolve(path)); err != nil {
				return err
			}
		}
	}
	seen := map[string]bool{}
	kinds := map[string]bool{}
	for _, s := range c.Scenarios {
		if !identifier.MatchString(s.ID) || seen[s.ID] {
			return fail("scenario IDs must be unique lowercase identifiers (max 63 characters)")
		}
		seen[s.ID] = true
		valid := s.Kind == "mixed"
		for _, kind := range RequiredKinds {
			if s.Kind == kind {
				valid = true
			}
		}
		if !valid {
			return fail("unknown scenario kind: " + s.Kind)
		}
		kinds[s.Kind] = true
		if s.Name == "" || len(s.Steps) == 0 {
			return fail("each scenario requires a name and steps")
		}
		vars := map[string]bool{}
		for _, step := range s.Steps {
			if step.Name == "" || (step.App != "old" && step.App != "new") {
				return fail("each step requires name and app old/new")
			}
			switch step.Method {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
			default:
				return fail("unsupported HTTP method: " + step.Method)
			}
			if !localPath(step.Path) {
				return fail("step path must be an absolute local HTTP path")
			}
			if step.Expect.Status < 100 || step.Expect.Status > 599 {
				return fail("every step requires expect.status (100..599)")
			}
			for ptr := range step.Expect.JSON {
				if !pointer(ptr) {
					return fail("expect.json keys must be JSON pointers")
				}
			}
			for _, value := range []any{step.Path, step.Body, step.Expect.JSON} {
				if err := validateVariables(value, vars); err != nil {
					return fmt.Errorf("configuration scenario %s step %s: %w", s.ID, step.Name, err)
				}
			}
			for name, ptr := range step.Capture {
				if !identifier.MatchString(name) || vars[name] || !pointer(ptr) {
					return fail("capture names must be unique identifiers and values JSON pointers")
				}
				vars[name] = true
			}
		}
		if err := validateShape(s); err != nil {
			return err
		}
	}
	for _, kind := range RequiredKinds {
		if !kinds[kind] {
			return fail("missing required scenario kind: " + kind)
		}
	}
	return nil
}
func localPath(s string) bool {
	return strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.ContainsAny(s, "\x00\r\n#\\")
}
func pointer(s string) bool {
	if s != "" && !strings.HasPrefix(s, "/") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '~' {
			if i+1 == len(s) || (s[i+1] != '0' && s[i+1] != '1') {
				return false
			}
			i++
		}
	}
	return true
}
func file(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("configuration input %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("configuration input is not a regular file: %s", path)
	}
	return nil
}

// Captures become available only after the capturing request completes.
// Request data and assertions must therefore refer to earlier captures.
func validateVariables(value any, vars map[string]bool) error {
	switch v := value.(type) {
	case string:
		for _, match := range variable.FindAllStringSubmatch(v, -1) {
			if !vars[match[1]] {
				return fmt.Errorf("undefined response variable %q", match[1])
			}
		}
		remainder := variable.ReplaceAllString(v, "")
		if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
			return fmt.Errorf("invalid variable expression; use {{name}} with a lowercase identifier")
		}
	case []any:
		for _, item := range v {
			if err := validateVariables(item, vars); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, item := range v {
			if err := validateVariables(item, vars); err != nil {
				return err
			}
		}
	case map[any]any:
		return fmt.Errorf("JSON objects require string keys")
	}
	if _, err := json.Marshal(value); err != nil {
		return fmt.Errorf("request and assertion data must be JSON-compatible: %w", err)
	}
	return nil
}
func validateShape(s Scenario) error {
	fail := func(msg string) error { return fmt.Errorf("configuration scenario %s: %s", s.ID, msg) }
	if s.Kind == "baseline" || s.Kind == "old_after" || s.Kind == "new_after" {
		want := "old"
		if s.Kind == "new_after" {
			want = "new"
		}
		for _, step := range s.Steps {
			if step.App != want {
				return fail("kind requires only " + want + " application steps")
			}
		}
		return nil
	}
	if s.Kind == "mixed" {
		return nil
	}
	writer, reader := "old", "new"
	if s.Kind == "new_to_old" {
		writer, reader = reader, writer
	}
	wrote := false
	for _, step := range s.Steps {
		if step.App == writer && (step.Method == "POST" || step.Method == "PUT" || step.Method == "PATCH" || step.Method == "DELETE") {
			wrote = true
		}
		if wrote && step.App == reader && step.Method == "GET" && len(step.Expect.JSON) > 0 {
			return nil
		}
	}
	return fail("cross-version scenario requires a writer mutation followed by the other version's GET with JSON assertions")
}

// Fingerprint detects changed SQL, configuration, and local app build inputs.
// Image digests are recorded separately in reports; remote services are not snapshotted.
func (c *Config) Fingerprint() (string, error) {
	h := sha256.New()
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	h.Write(data)
	paths := append([]string{c.Baseline, c.Seed}, c.Migrations...)
	for _, name := range []string{"old", "new"} {
		app := c.Apps[name]
		if app.Build != "" {
			root := c.Resolve(app.Build)
			reportRoot := ""
			if c.Report != "" {
				reportRoot = filepath.Clean(c.Resolve(c.Report))
			}
			if app.Dockerfile != "" {
				paths = append(paths, filepath.Join(root, app.Dockerfile))
			}
			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					if d.Name() == ".git" || d.Name() == ".tools" || d.Name() == ".releaseproof" || d.Name() == "bin" || (path != root && path == reportRoot) {
						return filepath.SkipDir
					}
					return nil
				}
				if d.Name() == "coverage.out" || d.Name() == "unit-tests.txt" || d.Name() == "integration-tests.txt" || strings.HasSuffix(d.Name(), ".test") || strings.HasSuffix(d.Name(), ".exe") {
					return nil
				}
				if d.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("build fingerprint does not support symlinks: %s", path)
				}
				paths = append(paths, path)
				return nil
			})
			if err != nil {
				return "", err
			}
		}
	}
	// Sort and deduplicate resolved paths so repeated build contexts have one stable
	// contribution. SQL order remains represented by the serialized configuration.
	resolvedPaths := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		resolved := filepath.Clean(c.Resolve(p))
		if !seen[resolved] {
			seen[resolved] = true
			resolvedPaths = append(resolvedPaths, resolved)
		}
	}
	sort.Strings(resolvedPaths)
	for _, resolved := range resolvedPaths {
		data, err := os.ReadFile(resolved)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(filepath.Dir(c.Path), resolved)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "\x00%s\x00%d\x00", filepath.ToSlash(relative), len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
