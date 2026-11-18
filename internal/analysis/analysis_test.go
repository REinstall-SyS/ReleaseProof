package analysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REinstall-SyS/ReleaseProof/internal/config"
)

func TestScanFindsActionableDDL(t *testing.T) {
	dir := t.TempDir()
	sql := `ALTER TABLE users DROP COLUMN full_name;
 ALTER TABLE users ALTER COLUMN age TYPE bigint;
 ALTER TABLE users ALTER COLUMN age SET NOT NULL;
 ALTER TABLE users RENAME COLUMN old_email TO email;
 ALTER TABLE users ADD COLUMN enabled boolean NOT NULL DEFAULT true;
 DROP TABLE retired_users;
 CREATE UNIQUE INDEX user_names ON users(first_name);`
	if err := os.WriteFile(filepath.Join(dir, "migration.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	c := &config.Config{Path: filepath.Join(dir, "releaseproof.yaml"), Migrations: []string{"migration.sql"}}
	risks := Scan(c)
	codes := map[string]bool{}
	for _, r := range risks {
		codes[r.Code] = true
		if r.File != "migration.sql" || !strings.HasPrefix(r.Message, "SQL text heuristic:") {
			t.Fatalf("missing provenance: %+v", r)
		}
	}
	for _, code := range []string{"drop-column", "drop-table", "rename-column", "alter-type", "not-null", "added-not-null", "alter-table-lock", "index-write-lock"} {
		if !codes[code] {
			t.Errorf("missing warning %s in %+v", code, risks)
		}
	}
}

func TestScanIgnoresCommentsStringsAndFunctionBodies(t *testing.T) {
	dir := t.TempDir()
	sql := `-- DROP TABLE users;
 /* ALTER TABLE x DROP COLUMN y; /* SET NOT NULL */ */
 SELECT 'DROP COLUMN full_name', E'escaped\' DROP TABLE fake';
 SELECT "DROP TABLE quoted_identifier";
 DO $body$ BEGIN EXECUTE 'DROP TABLE users'; END $body$;
 CREATE INDEX CONCURRENTLY user_names ON users(full_name);`
	if err := os.WriteFile(filepath.Join(dir, "migration.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	risks := Scan(&config.Config{Path: filepath.Join(dir, "releaseproof.yaml"), Migrations: []string{"migration.sql"}})
	if len(risks) != 0 {
		t.Fatalf("non-DDL text caused warnings: %+v", risks)
	}
}

func TestMaskNestedCommentsAndDollarStrings(t *testing.T) {
	input := `/* outer /* inner */ outer */ SELECT $$DROP TABLE users$$; ALTER TABLE "users" DROP COLUMN "full_name";`
	got, complete := mask(input)
	if !complete || strings.Contains(got, "DROP TABLE") || !strings.Contains(got, `ALTER TABLE "     " DROP COLUMN "         "`) {
		t.Fatalf("wrong mask: %q (%v)", got, complete)
	}
	if len(got) != len(input) {
		t.Fatal("mask changed byte positions")
	}
	for _, input := range []string{"/* incomplete", "SELECT 'incomplete", "DO $tag$ incomplete", `SELECT "incomplete`} {
		if _, complete := mask(input); complete {
			t.Errorf("unterminated input considered complete: %q", input)
		}
	}
}

func TestMissingSQLProducesExplicitAnalysisWarning(t *testing.T) {
	risks := Scan(&config.Config{Path: filepath.Join(t.TempDir(), "releaseproof.yaml"), Migrations: []string{"missing.sql"}})
	if len(risks) != 1 || risks[0].Code != "analysis-unavailable" {
		t.Fatalf("got %+v", risks)
	}
}
