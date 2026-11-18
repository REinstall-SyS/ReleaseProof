package postgres

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDSNEscapesCredentials(t *testing.T) {
	raw := DSN("127.0.0.1:5432", "isolated", "app", "p@ss:/word")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if password != "p@ss:/word" || u.Query().Get("sslmode") != "disable" {
		t.Fatal(raw)
	}
}
func TestSecretsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		secret := Secret()
		if len(secret) != 36 || seen[secret] {
			t.Fatal("invalid or duplicate random secret")
		}
		seen[secret] = true
	}
}
func TestSQLFileRejectsHostMetaCommands(t *testing.T) {
	for _, sql := range []string{"\\! echo unsafe", "SELECT 1;\n\\connect other", "COPY users FROM STDIN;"} {
		path := filepath.Join(t.TempDir(), "migration.sql")
		if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := SQLFile(path); err == nil {
			t.Fatalf("accepted unsupported script: %s", sql)
		}
	}
}
func TestSQLFilePreservesFunctionBoundaries(t *testing.T) {
	sql := "CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN PERFORM 1; END $$;\nCREATE INDEX CONCURRENTLY idx ON users(id);"
	path := filepath.Join(t.TempDir(), "migration.sql")
	if err := os.WriteFile(path, []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := SQLFile(path)
	if err != nil || got != sql {
		t.Fatalf("sql changed: %s %v", got, err)
	}
}
func TestCatalogDiffUsesObjectIdentity(t *testing.T) {
	before := []Column{{Table: "users", TableOID: 1, Number: 1, Name: "id", Type: "integer"}, {Table: "users", TableOID: 1, Number: 2, Name: "full_name", Type: "text"}, {Table: "old", TableOID: 2, Number: 1, Name: "id"}}
	after := []Column{{Table: "users", TableOID: 1, Number: 1, Name: "user_id", Type: "bigint"}, {Table: "users", TableOID: 1, Number: 3, Name: "first_name", Type: "text"}}
	risks := Diff(before, after)
	codes := map[string]bool{}
	for _, risk := range risks {
		codes[risk.Code] = true
		if !strings.Contains(risk.Message, "Observed catalog") {
			t.Fatal(risk)
		}
	}
	for _, code := range []string{"catalog_rename_column", "catalog_type_change", "catalog_drop_column", "catalog_drop_table"} {
		if !codes[code] {
			t.Fatalf("missing %s: %+v", code, risks)
		}
	}
}
