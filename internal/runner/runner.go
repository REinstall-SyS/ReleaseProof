// Package runner orchestrates isolated databases and real application containers.
package runner

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/REinstall-SyS/ReleaseProof/internal/analysis"
	"github.com/REinstall-SyS/ReleaseProof/internal/assertions"
	"github.com/REinstall-SyS/ReleaseProof/internal/config"
	"github.com/REinstall-SyS/ReleaseProof/internal/docker"
	"github.com/REinstall-SyS/ReleaseProof/internal/postgres"
	"github.com/REinstall-SyS/ReleaseProof/internal/reporting"
)

type Options struct {
	Selected       string
	Output         string
	ExpectedImages map[string]string
	// Observe is optional progress reporting; it never changes the verdict.
	Observe func(string)
}

func Run(ctx context.Context, c *config.Config, options Options) (report reporting.Report) {
	start := time.Now()
	report = reporting.Report{Version: 1, RunID: postgres.Secret()[:16], Config: c.Path, StartedAt: start.UTC(), Status: "error", MigrationFiles: c.Migrations, Images: map[string]string{}, Timings: map[string]int64{}, Risks: analysis.Scan(c)}
	redactor := reporting.NewRedactor()
	for _, app := range c.Apps {
		for key, value := range app.Env {
			if sensitive(key) {
				redactor.Add(value)
			}
		}
	}
	defer func() { report.DurationMS = time.Since(start).Milliseconds(); report = redactor.Clean(report) }()
	if err := c.Validate(); err != nil {
		report.Error = err.Error()
		return
	}
	fingerprint, err := c.Fingerprint()
	if err != nil {
		report.Error = "fingerprint inputs: " + err.Error()
		return
	}
	report.Fingerprint = fingerprint
	plan, err := Plan(c, options.Selected)
	if err != nil {
		report.Error = err.Error()
		return
	}
	setupStart := time.Now()
	setupCtx, setupCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer setupCancel()
	engine, err := docker.New(setupCtx, report.RunID)
	if err != nil {
		report.Error = err.Error()
		return
	}
	defer func() {
		cleanupStart := time.Now()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := engine.Cleanup(cleanupCtx); err != nil {
			report.Error = join(report.Error, "cleanup: "+err.Error())
			report.Status = "error"
		}
		report.Timings["cleanup_ms"] = time.Since(cleanupStart).Milliseconds()
	}()
	progress := func(s string) {
		if options.Observe != nil {
			options.Observe(redactor.String(s))
		}
	}
	progress("Preparing run-owned Docker network and images")
	if err := engine.CreateNetwork(setupCtx); err != nil {
		report.Error = err.Error()
		return
	}
	images := map[string]string{}
	for _, version := range []string{"postgres", "old", "new"} {
		app := c.Apps[version]
		if version == "postgres" {
			app = config.App{Image: c.Postgres.Image}
		}
		image, digest, err := engine.PrepareImage(setupCtx, version, app, c.Resolve)
		if err != nil {
			report.Error = version + " image: " + err.Error()
			return
		}
		images[version] = image
		report.Images[version] = digest
		if expected := options.ExpectedImages[version]; expected != "" && expected != digest {
			report.Error = fmt.Sprintf("reproduction image mismatch for %s: recorded %s; available %s; restore the recorded image or use a fresh check", version, expected, digest)
			return
		}
	}
	adminPassword := postgres.Secret()
	redactor.Add(adminPassword)
	pgName := "releaseproof-" + report.RunID + "-pg"
	address, err := engine.CreateContainer(setupCtx, pgName, images["postgres"], 5432, map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": adminPassword, "POSTGRES_DB": "postgres"}, nil, true)
	if err != nil {
		report.Error = "PostgreSQL container: " + err.Error()
		return
	}
	readyCtx, cancel := context.WithTimeout(setupCtx, c.StartupDuration())
	admin, err := postgres.Connect(readyCtx, postgres.DSN(address, "postgres", "postgres", adminPassword))
	cancel()
	if err != nil {
		report.Error = err.Error() + "\n" + engine.CombinedLogs(setupCtx, pgName)
		return
	}
	defer admin.Close(context.Background())
	report.Timings["setup_ms"] = time.Since(setupStart).Milliseconds()
	setupCancel()
	baselineOK := true
	for i, s := range plan {
		result := reporting.Scenario{ID: s.ID, Name: s.Name, Kind: s.Kind, Status: "skipped", Versions: map[string]string{}, Reproduce: reproductionCommand(c, options, report.RunID, s.ID)}
		for _, step := range s.Steps {
			result.Versions[step.App] = c.Apps[step.App].Version
		}
		if !baselineOK {
			result.Error = "not run: baseline checks must pass before migration comparison"
			report.Scenarios = append(report.Scenarios, result)
			continue
		}
		if ctx.Err() != nil {
			result.Status = "error"
			result.Error = ctx.Err().Error()
			report.Scenarios = append(report.Scenarios, result)
			continue
		}
		progress("Running " + s.ID + ": " + s.Name)
		started := time.Now()
		scenarioCtx, scenarioCancel := context.WithTimeout(ctx, c.Duration())
		db, createErr := postgres.Create(scenarioCtx, admin, i)
		redactor.Add(db.OwnerPassword, db.AppPassword)
		if createErr != nil {
			result.Status = "error"
			result.Error = "create isolated database: " + createErr.Error()
		} else {
			result = execute(scenarioCtx, c, engine, pgName, address, images, db, s, result)
		}
		scenarioCancel()
		result.DurationMS = time.Since(started).Milliseconds()
		if s.Kind == "baseline" && result.Status != "pass" {
			baselineOK = false
		}
		report.Scenarios = append(report.Scenarios, result)
	}
	report.Status = Verdict(report.Scenarios)
	if !baselineOK && report.Status != "error" {
		report.Status = "baseline_failed"
	}
	if ctx.Err() != nil {
		report.Status = "error"
		report.Error = join(report.Error, ctx.Err().Error())
	}
	return
}
func execute(ctx context.Context, c *config.Config, engine *docker.Client, pgName, address string, images map[string]string, db postgres.Database, s config.Scenario, result reporting.Scenario) (out reporting.Scenario) {
	out = result
	out.Status = "error"
	out.Logs = map[string]string{}
	names := []string{}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if out.Status != "pass" {
			for _, name := range names {
				out.Logs[name] = engine.CombinedLogs(cleanupCtx, name)
			}
			out.Logs["postgres"] = engine.CombinedLogs(cleanupCtx, pgName)
		}
		if err := engine.Remove(cleanupCtx, names...); err != nil {
			out.Status = "error"
			out.Error = join(out.Error, "application cleanup: "+err.Error())
		}
	}()
	apply := func(file string) error {
		if file == "" {
			return nil
		}
		sql, err := postgres.SQLFile(c.Resolve(file))
		if err != nil {
			return err
		}
		if err = engine.SQL(ctx, pgName, db.Name, db.Owner, db.OwnerPassword, sql, c.Duration()); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		return nil
	}
	if err := apply(c.Baseline); err != nil {
		out.Error = "baseline SQL: " + err.Error()
		return
	}
	if err := apply(c.Seed); err != nil {
		out.Error = "seed SQL: " + err.Error()
		return
	}
	if s.Kind != "baseline" {
		before, err := postgres.Snapshot(ctx, address, db)
		if err != nil {
			out.Error = err.Error()
			return
		}
		migrationStart := time.Now()
		for _, file := range c.Migrations {
			if err := apply(file); err != nil {
				out.MigrationMS = time.Since(migrationStart).Milliseconds()
				out.Error = "migration: " + err.Error()
				return
			}
		}
		out.MigrationMS = time.Since(migrationStart).Milliseconds()
		after, err := postgres.Snapshot(ctx, address, db)
		if err != nil {
			out.Error = err.Error()
			return
		}
		// Store observed DDL as evidence, without turning catalog changes into a
		// compatibility verdict: only the developer's executed checks decide that.
		var messages []string
		for _, risk := range postgres.Diff(before, after) {
			messages = append(messages, risk.Code+": "+risk.Message)
		}
		if len(messages) > 0 {
			out.Logs["catalog"] = strings.Join(messages, "\n")
		}
	}
	if err := postgres.Grant(ctx, address, db); err != nil {
		out.Error = "grant application DML privileges: " + err.Error()
		return
	}
	bases := map[string]string{}
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 4, IdleConnTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: c.RequestDuration(), Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, step := range s.Steps {
		version := step.App
		if bases[version] != "" {
			continue
		}
		app := c.Apps[version]
		env := map[string]string{}
		for k, v := range app.Env {
			env[k] = v
		}
		env["PORT"] = strconv.Itoa(app.Port)
		env["DATABASE_URL"] = postgres.DSN("postgres:5432", db.Name, db.AppUser, db.AppPassword)
		name := "releaseproof-" + engine.ID + "-" + db.Name + "-" + version
		names = append(names, name)
		addr, err := engine.CreateContainer(ctx, name, images[version], app.Port, env, app.Command, false)
		if err != nil {
			out.Error = version + " application start: " + err.Error()
			return
		}
		base := "http://" + addr
		startup := c.StartupDuration()
		if app.StartupTimeout != "" {
			startup = app.StartupDuration()
		}
		readyCtx, cancel := context.WithTimeout(ctx, startup)
		err = waitHTTP(readyCtx, client, base+app.Health, func() bool { return engine.Running(readyCtx, name) })
		cancel()
		if err != nil {
			out.Error = version + " application readiness: " + err.Error()
			return
		}
		bases[version] = base
	}
	vars := assertions.Variables{}
	for _, step := range s.Steps {
		evidence, err := assertions.Run(ctx, client, bases[step.App], step, vars)
		out.Steps = append(out.Steps, evidence)
		if err != nil {
			out.Error = err.Error()
			out.Status = "fail"
			if evidence.Status == 0 || ctx.Err() != nil {
				out.Status = "error"
			}
			return
		}
	}
	out.Status = "pass"
	return
}
func waitHTTP(ctx context.Context, client *http.Client, url string, running func() bool) error {
	last := "no HTTP response"
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("%w; %s", ctx.Err(), last)
		}
		if !running() {
			return fmt.Errorf("container exited before health check passed; inspect recorded application logs")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Sprintf("health returned HTTP %d", response.StatusCode)
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w; %s", ctx.Err(), last)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Verdict prioritizes execution failures over incompatibilities. A missing or
// skipped result is never evidence of compatibility.
func Verdict(scenarios []reporting.Scenario) string {
	if len(scenarios) == 0 {
		return "error"
	}
	failed, baseline := false, false
	for _, s := range scenarios {
		if s.Status != "pass" && s.Status != "fail" && s.Status != "error" && s.Status != "skipped" && s.Status != "unsupported" {
			return "error"
		}
		if s.Status == "error" || s.Status == "unsupported" {
			return "error"
		}
		if s.Kind == "baseline" && s.Status != "pass" {
			baseline = true
		}
		if s.Status == "fail" {
			failed = true
		}
		if s.Status == "skipped" && !baseline {
			return "error"
		}
	}
	if baseline {
		return "baseline_failed"
	}
	if failed {
		return "incompatible"
	}
	return "compatible"
}
func reproductionCommand(c *config.Config, o Options, run, id string) string {
	output := o.Output
	if output == "" {
		output = c.Resolve(c.Report)
	}
	reportPath := filepath.Join(output, run, "report.json")
	return "releaseproof reproduce " + id + " --report " + strconv.Quote(filepath.ToSlash(reportPath))
}
func sensitive(key string) bool {
	upper := strings.ToUpper(key)
	for _, part := range []string{"PASSWORD", "SECRET", "TOKEN", "KEY", "CREDENTIAL", "DSN", "DATABASE_URL"} {
		if strings.Contains(upper, part) {
			return true
		}
	}
	return false
}
func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
