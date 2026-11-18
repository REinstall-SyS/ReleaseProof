//go:build integration

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/REinstall-SyS/ReleaseProof/internal/config"
	"github.com/REinstall-SyS/ReleaseProof/internal/docker"
	"github.com/REinstall-SyS/ReleaseProof/internal/postgres"
	"github.com/REinstall-SyS/ReleaseProof/internal/reporting"
)

// These tests deliberately use real PostgreSQL and real HTTP application
// containers. Selecting the integration tag requires a working local Linux
// Docker daemon; a missing daemon is a test failure, not a skipped verification.
func TestIntegrationCompatibilityMatrix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		status string
		code   int
	}{{"expand-and-contract", "safe.yaml", "compatible", 0}, {"removed-column", "unsafe.yaml", "incompatible", 1}, {"successful-http-but-stale-data", "semantic.yaml", "incompatible", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			c := loadDemo(t, tc.config)
			report := runContainerCheck(t, c, Options{Output: t.TempDir()})
			if report.Status != tc.status || reporting.ExitCode(report) != tc.code {
				t.Fatalf("unexpected verdict: status=%s exit=%d error=%s scenarios=%+v", report.Status, reporting.ExitCode(report), report.Error, report.Scenarios)
			}
			if len(report.Scenarios) != len(c.Scenarios) {
				t.Fatalf("executed %d scenarios, want %d", len(report.Scenarios), len(c.Scenarios))
			}
			assertScenarioStatus(t, report, "baseline", "pass")
			assertScenarioStatus(t, report, "new-after", "pass")
			switch tc.config {
			case "safe.yaml":
				for _, scenario := range report.Scenarios {
					if scenario.Status != "pass" || len(scenario.Steps) == 0 {
						t.Errorf("scenario has no passing executed evidence: %+v", scenario)
					}
				}
				assertScenarioStatus(t, report, "mixed-update", "pass")
			case "unsafe.yaml":
				old := assertScenarioStatus(t, report, "old-after", "fail")
				if len(old.Steps) != 1 || old.Steps[0].Status != 500 || !strings.Contains(old.Steps[0].Body, "full_name") {
					t.Fatalf("missing actual PostgreSQL/HTTP error evidence: %+v", old)
				}
				if old.Logs["catalog"] == "" || !strings.Contains(old.Logs["catalog"], "catalog_drop_column") {
					t.Fatalf("missing observed catalog removal: %+v", old.Logs)
				}
			case "semantic.yaml":
				assertScenarioStatus(t, report, "old-after", "pass")
				for _, id := range []string{"old-to-new", "new-to-old"} {
					failed := assertScenarioStatus(t, report, id, "fail")
					if len(failed.Steps) != 2 || failed.Steps[0].Status != 200 || failed.Steps[1].Status != 200 || !strings.Contains(failed.Error, "Lovelace") {
						t.Fatalf("expected isolated stale-read assertion despite HTTP 200: %+v", failed)
					}
				}
			}
			assertReportArtifacts(t, report)
		})
	}
}

func TestIntegrationInvalidMigrationAndCleanup(t *testing.T) {
	c := loadDemo(t, "safe.yaml")
	c.Migrations = []string{writeSQL(t, "ALTER TABLE users ADD COLUMN broken ; this is not valid SQL;")}
	report := runContainerCheck(t, c, Options{Selected: "old-after", Output: t.TempDir()})
	if report.Status != "error" || reporting.ExitCode(report) != 3 {
		t.Fatalf("invalid SQL was not an execution failure: %+v", report)
	}
	assertScenarioStatus(t, report, "baseline", "pass")
	failed := assertScenarioStatus(t, report, "old-after", "error")
	if !strings.Contains(failed.Error, "migration:") || !strings.Contains(strings.ToLower(failed.Error), "syntax error") || len(failed.Steps) != 0 {
		t.Fatalf("migration error lacks actual SQL diagnostics or application ran after failure: %+v", failed)
	}
}

func TestIntegrationNontransactionalDDL(t *testing.T) {
	c := loadDemo(t, "safe.yaml")
	c.Migrations = []string{writeSQL(t, "CREATE INDEX CONCURRENTLY users_full_name_idx ON users (full_name);\n")}
	report := runContainerCheck(t, c, Options{Selected: "old-after", Output: t.TempDir()})
	if report.Status != "compatible" {
		t.Fatalf("valid nontransactional DDL failed: %+v", report)
	}
	assertScenarioStatus(t, report, "old-after", "pass")
}

func TestIntegrationMissingApplicationDependency(t *testing.T) {
	c := loadDemo(t, "safe.yaml")
	app := c.Apps["old"]
	app.Build, app.Dockerfile = "", ""
	app.Image = c.Postgres.Image
	// The PostgreSQL entrypoint execs its command; scratch demo ENTRYPOINT /app
	// would treat command values as arguments instead of replacing the binary.
	app.Command = []string{"/releaseproof-missing-dependency"}
	app.StartupTimeout = "2s"
	c.Apps["old"] = app
	report := runContainerCheck(t, c, Options{Selected: "baseline", Output: t.TempDir()})
	if report.Status != "error" {
		t.Fatalf("missing application executable was not infrastructure failure: %+v", report)
	}
	failed := assertScenarioStatus(t, report, "baseline", "error")
	if !strings.Contains(failed.Error, "application") || len(failed.Steps) != 0 {
		t.Fatalf("application failure was not collected before checks: %+v", failed)
	}
}

func TestIntegrationApplicationStartupTimeout(t *testing.T) {
	c := loadDemo(t, "safe.yaml")
	app := c.Apps["old"]
	app.Health = "/releaseproof-no-such-health-route"
	app.StartupTimeout = "300ms"
	c.Apps["old"] = app
	report := runContainerCheck(t, c, Options{Selected: "baseline", Output: t.TempDir()})
	if report.Status != "error" {
		t.Fatalf("startup deadline was not infrastructure failure: %+v", report)
	}
	failed := assertScenarioStatus(t, report, "baseline", "error")
	if !strings.Contains(failed.Error, "readiness") || !strings.Contains(failed.Error, "deadline exceeded") {
		t.Fatalf("deadline evidence missing: %+v", failed)
	}
}

func TestIntegrationDatabaseConnectionFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	conn, err := postgres.Connect(ctx, postgres.DSN("127.0.0.1:1", "releaseproof_unavailable", "application", "connection-failure-secret"))
	if conn != nil {
		conn.Close(context.Background())
		t.Fatal("unexpected PostgreSQL listener at 127.0.0.1:1")
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "PostgreSQL readiness") || strings.Contains(err.Error(), "connection-failure-secret") {
		t.Fatalf("unhelpful or credential-bearing connection error: %v", err)
	}
}

func TestIntegrationApplicationPrivilegesAreRestricted(t *testing.T) {
	c := loadDemo(t, "safe.yaml")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id := postgres.Secret()[:16]
	engine, err := docker.New(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := engine.Cleanup(cleanupCtx); err != nil {
			t.Errorf("privilege test cleanup: %v", err)
		}
		assertNoOwnedResources(t, id)
	}()
	if err := engine.CreateNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	image, _, err := engine.PrepareImage(ctx, "postgres", config.App{Image: c.Postgres.Image}, c.Resolve)
	if err != nil {
		t.Fatal(err)
	}
	password := postgres.Secret()
	address, err := engine.CreateContainer(ctx, "releaseproof-"+id+"-pg", image, 5432, map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": password, "POSTGRES_DB": "postgres"}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 30*time.Second)
	admin, err := postgres.Connect(readyCtx, postgres.DSN(address, "postgres", "postgres", password))
	readyCancel()
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	db, err := postgres.Create(ctx, admin, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Defense in depth: even a script handed directly to the execution layer
	// cannot invoke psql metacommands. The SQL-file lexer is not the sole guard.
	if err := engine.SQL(ctx, "releaseproof-"+id+"-pg", db.Name, db.Owner, db.OwnerPassword, "SELECT 1;\n\\echo forbidden\n", 5*time.Second); err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("psql restricted mode did not reject metacommand: %v", err)
	}
	owner, err := pgx.Connect(ctx, postgres.DSN(address, db.Name, db.Owner, db.OwnerPassword))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(context.Background())
	assertRestrictedRole(t, ctx, owner)
	if _, err := owner.Exec(ctx, "CREATE TABLE privilege_probe (id integer PRIMARY KEY, value text NOT NULL)"); err != nil {
		t.Fatalf("non-superuser migration owner cannot create its schema: %v", err)
	}
	if err := postgres.Grant(ctx, address, db); err != nil {
		t.Fatal(err)
	}
	application, err := pgx.Connect(ctx, postgres.DSN(address, db.Name, db.AppUser, db.AppPassword))
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close(context.Background())
	assertRestrictedRole(t, ctx, application)
	if _, err := application.Exec(ctx, "INSERT INTO privilege_probe VALUES (1,'before'); UPDATE privilege_probe SET value='after' WHERE id=1"); err != nil {
		t.Fatalf("application role cannot perform ordinary DML: %v", err)
	}
	var value string
	if err := application.QueryRow(ctx, "SELECT value FROM privilege_probe WHERE id=1").Scan(&value); err != nil || value != "after" {
		t.Fatalf("application DML did not persist: value=%q err=%v", value, err)
	}
	for _, sql := range []string{"CREATE TABLE forbidden_table (id integer)", "ALTER TABLE privilege_probe ADD COLUMN forbidden text", "CREATE ROLE forbidden_role", "CREATE DATABASE forbidden_database", "SELECT rolpassword FROM pg_authid"} {
		if _, err := application.Exec(ctx, sql); err == nil {
			t.Errorf("application role unexpectedly permitted privileged operation: %s", sql)
		}
	}
	other, err := postgres.Create(ctx, admin, 1)
	if err != nil {
		t.Fatal(err)
	}
	crossScenario, err := pgx.Connect(ctx, postgres.DSN(address, other.Name, db.AppUser, db.AppPassword))
	if crossScenario != nil {
		crossScenario.Close(context.Background())
	}
	if err == nil {
		t.Error("one scenario's application role could connect to another scenario's database")
	}
}

func assertRestrictedRole(t *testing.T, ctx context.Context, connection *pgx.Conn) {
	t.Helper()
	var superuser, createDB, createRole, replication bool
	err := connection.QueryRow(ctx, "SELECT rolsuper,rolcreatedb,rolcreaterole,rolreplication FROM pg_roles WHERE rolname=current_user").Scan(&superuser, &createDB, &createRole, &replication)
	if err != nil || superuser || createDB || createRole || replication {
		t.Fatalf("role is not appropriately restricted: superuser=%v createdb=%v createrole=%v replication=%v err=%v", superuser, createDB, createRole, replication, err)
	}
}

func TestIntegrationBrokenBaselineGatesMigratedScenarios(t *testing.T) {
	c := loadDemo(t, "safe.yaml")
	secret := "releaseproof-integration-private-token"
	app := c.Apps["old"]
	app.Env = map[string]string{"API_TOKEN": secret}
	c.Apps["old"] = app
	for i := range c.Scenarios {
		if c.Scenarios[i].Kind == "baseline" {
			c.Scenarios[i].Steps[0].Expect.JSON["/full_name"] = secret
		}
	}
	report := runContainerCheck(t, c, Options{Output: t.TempDir()})
	if report.Status != "baseline_failed" || reporting.ExitCode(report) != 3 {
		t.Fatalf("broken baseline incorrectly implicated migration: %+v", report)
	}
	assertScenarioStatus(t, report, "baseline", "fail")
	for _, scenario := range report.Scenarios {
		if scenario.Kind != "baseline" && (scenario.Status != "skipped" || len(scenario.Steps) != 0 || scenario.MigrationMS != 0) {
			t.Errorf("migration proceeded after broken baseline: %+v", scenario)
		}
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("configured secret leaked or no actual secret-bearing assertion was captured: %s", data)
	}
}

func TestIntegrationInterruptedExecutionCleansResources(t *testing.T) {
	c := loadDemo(t, "safe.yaml")
	c.Migrations = []string{writeSQL(t, "SELECT pg_sleep(30);\n")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	interrupted := false
	report := Run(ctx, c, Options{Selected: "old-after", Output: t.TempDir(), Observe: func(message string) {
		if strings.HasPrefix(message, "Running old-after:") {
			interrupted = true
			time.AfterFunc(2*time.Second, cancel)
		}
	}})
	assertNoOwnedResources(t, report.RunID)
	if !interrupted || report.Status != "error" || !strings.Contains(report.Error, "context canceled") {
		t.Fatalf("execution was not interrupted after PostgreSQL/baseline startup: %+v", report)
	}
	assertScenarioStatus(t, report, "baseline", "pass")
	assertScenarioStatus(t, report, "old-after", "error")
}

func TestIntegrationConcurrentExecutionsAreIsolated(t *testing.T) {
	configs := []*config.Config{loadDemo(t, "safe.yaml"), loadDemo(t, "safe.yaml")}
	outputs := []string{t.TempDir(), t.TempDir()}
	reports := make([]reporting.Report, 2)
	var workers sync.WaitGroup
	for i := range configs {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			reports[index] = Run(ctx, configs[index], Options{Selected: "old-to-new", Output: outputs[index]})
		}(i)
	}
	workers.Wait()
	if reports[0].RunID == reports[1].RunID || reports[0].RunID == "" || reports[1].RunID == "" {
		t.Fatalf("concurrent runs did not get distinct ownership IDs: %q %q", reports[0].RunID, reports[1].RunID)
	}
	for _, report := range reports {
		assertNoOwnedResources(t, report.RunID)
		if report.Status != "compatible" {
			t.Fatalf("concurrent isolated check failed: %+v", report)
		}
		assertScenarioStatus(t, report, "baseline", "pass")
		assertScenarioStatus(t, report, "old-to-new", "pass")
	}
}

func TestIntegrationReproductionRerunsRecordedFailure(t *testing.T) {
	c := loadDemo(t, "unsafe.yaml")
	first := runContainerCheck(t, c, Options{Selected: "old-after", Output: t.TempDir()})
	if first.Status != "incompatible" {
		t.Fatalf("unsafe fixture failed to produce original incompatibility: %+v", first)
	}
	second := runContainerCheck(t, c, Options{Selected: "old-after", Output: t.TempDir(), ExpectedImages: first.Images})
	if second.Status != "incompatible" || second.Fingerprint != first.Fingerprint || second.RunID == first.RunID {
		t.Fatalf("recorded images/input did not reproduce in a fresh run: first=%+v second=%+v", first, second)
	}
	failure := assertScenarioStatus(t, second, "old-after", "fail")
	if len(failure.Steps) != 1 || failure.Steps[0].Status != 500 || !strings.Contains(failure.Steps[0].Body, "full_name") {
		t.Fatalf("different reproduction failure: %+v", failure)
	}
}

func loadDemo(t *testing.T, name string) *config.Config {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "examples", "rolling-deployment", name))
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("load demo %s: %v", name, err)
	}
	return c
}

func runContainerCheck(t *testing.T, c *config.Config, options Options) reporting.Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report := Run(ctx, c, options)
	assertNoOwnedResources(t, report.RunID)
	return report
}

func assertScenarioStatus(t *testing.T, report reporting.Report, id, status string) reporting.Scenario {
	t.Helper()
	for _, scenario := range report.Scenarios {
		if scenario.ID == id {
			if scenario.Status != status {
				t.Fatalf("scenario %s status=%s want=%s; error=%s logs=%+v", id, scenario.Status, status, scenario.Error, scenario.Logs)
			}
			return scenario
		}
	}
	t.Fatalf("scenario %s missing from executed report: %+v", id, report)
	return reporting.Scenario{}
}

func assertNoOwnedResources(t *testing.T, id string) {
	t.Helper()
	if id == "" {
		t.Fatal("run did not record an ownership ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	containers, err := docker.OwnedResources(ctx, id)
	if err != nil || len(containers) != 0 {
		t.Fatalf("run-owned containers leaked: %v; verification error=%v", containers, err)
	}
	for _, args := range [][]string{{"network", "ls", "--quiet", "--filter", "label=" + docker.Label + "=" + id}, {"image", "ls", "--quiet", "--filter", "reference=releaseproof-" + id + "-*"}} {
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "" {
			t.Fatalf("run-owned resources leaked after docker %v: %s; error=%v", args, output, err)
		}
	}
}

func assertReportArtifacts(t *testing.T, report reporting.Report) {
	t.Helper()
	path, err := reporting.Write(t.TempDir(), report)
	if err != nil {
		t.Fatal(err)
	}
	read, err := reporting.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Status != report.Status || len(read.Scenarios) != len(report.Scenarios) || read.RunID != report.RunID || read.Fingerprint != report.Fingerprint {
		t.Fatalf("JSON report does not match execution: %+v", read)
	}
	html, err := os.ReadFile(filepath.Join(filepath.Dir(path), "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	for i, scenario := range report.Scenarios {
		if read.Scenarios[i].Status != scenario.Status || len(read.Scenarios[i].Steps) != len(scenario.Steps) || !strings.Contains(string(html), scenario.ID) || !strings.Contains(string(html), strings.ToUpper(scenario.Status)) {
			t.Fatalf("rendered evidence does not match scenario %s", scenario.ID)
		}
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	credentialURL := regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s/@]+:[^\s/@]+@`)
	if credentialURL.Match(data) {
		t.Fatalf("report contains an unredacted database credential URL")
	}
}

func writeSQL(t *testing.T, sql string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migration.sql")
	if err := os.WriteFile(path, []byte(sql), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
