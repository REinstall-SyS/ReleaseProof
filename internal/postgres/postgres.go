// Package postgres manages only databases inside a run-owned PostgreSQL container.
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/REinstall-SyS/ReleaseProof/internal/reporting"
	"github.com/jackc/pgx/v5"
)

func Secret() string {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
func DSN(address, database, user, password string) string {
	u := url.URL{Scheme: "postgres", Host: address, Path: "/" + database, User: url.UserPassword(user, password)}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}
func Connect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	var last error
	for {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			err = conn.Ping(ctx)
			if err == nil {
				return conn, nil
			}
			_ = conn.Close(context.Background())
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("PostgreSQL readiness: %w (last connection error: %v)", ctx.Err(), last)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

type Database struct{ Name, Owner, OwnerPassword, AppUser, AppPassword string }

func Create(ctx context.Context, admin *pgx.Conn, index int) (Database, error) {
	suffix := fmt.Sprintf("%d", index)
	db := Database{Name: "scenario_" + suffix, Owner: "migration_" + suffix, OwnerPassword: Secret(), AppUser: "app_" + suffix, AppPassword: Secret()}
	// Names and passwords are generated internally, and identifiers are quoted.
	for _, role := range [][2]string{{db.Owner, db.OwnerPassword}, {db.AppUser, db.AppPassword}} {
		sql := "CREATE ROLE " + pgx.Identifier{role[0]}.Sanitize() + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '" + role[1] + "'"
		if _, err := admin.Exec(ctx, sql); err != nil {
			return db, err
		}
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db.Name}.Sanitize()+" OWNER "+pgx.Identifier{db.Owner}.Sanitize()+" TEMPLATE template0"); err != nil {
		return db, err
	}
	if _, err := admin.Exec(ctx, "REVOKE CONNECT,TEMPORARY ON DATABASE "+pgx.Identifier{db.Name}.Sanitize()+" FROM PUBLIC"); err != nil {
		return db, err
	}
	_, err := admin.Exec(ctx, "GRANT CONNECT ON DATABASE "+pgx.Identifier{db.Name}.Sanitize()+" TO "+pgx.Identifier{db.AppUser}.Sanitize())
	return db, err
}
func Grant(ctx context.Context, address string, db Database) error {
	conn, err := pgx.Connect(ctx, DSN(address, db.Name, db.Owner, db.OwnerPassword))
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	role := pgx.Identifier{db.AppUser}.Sanitize()
	_, err = conn.Exec(ctx, "REVOKE CREATE ON SCHEMA public FROM PUBLIC; GRANT USAGE ON SCHEMA public TO "+role+"; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO "+role+"; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO "+role)
	return err
}

var metaCommand = regexp.MustCompile(`(?m)^\s*\\`)
var copyStdin = regexp.MustCompile(`(?is)\bCOPY\b[^;]*\bFROM\s+STDIN\b`)

// SQLFile intentionally accepts SQL only. psql handles statement boundaries,
// dollar-quoted functions and nontransactional DDL; we do not split SQL ourselves.
func SQLFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sql := string(data)
	if metaCommand.MatchString(sql) {
		return "", fmt.Errorf("unsupported psql metacommand in %s: provide SQL-only files", path)
	}
	if copyStdin.MatchString(sql) {
		return "", fmt.Errorf("unsupported COPY FROM STDIN in %s: use INSERT fixtures or SQL COPY with a container-local source", path)
	}
	return sql, nil
}

type Column struct {
	Table      string
	TableOID   uint32
	Number     int16
	Name, Type string
	NotNull    bool
}

func Snapshot(ctx context.Context, address string, db Database) ([]Column, error) {
	conn, err := pgx.Connect(ctx, DSN(address, db.Name, db.Owner, db.OwnerPassword))
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, `SELECT c.relname,c.oid,a.attnum,a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid
 WHERE n.nspname='public' AND c.relkind IN ('r','p') AND a.attnum>0 AND NOT a.attisdropped ORDER BY c.oid,a.attnum`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Column
	for rows.Next() {
		var col Column
		if err := rows.Scan(&col.Table, &col.TableOID, &col.Number, &col.Name, &col.Type, &col.NotNull); err != nil {
			return nil, err
		}
		result = append(result, col)
	}
	return result, rows.Err()
}
func Diff(before, after []Column) []reporting.Risk {
	key := func(c Column) string { return fmt.Sprintf("%d:%d", c.TableOID, c.Number) }
	now := map[string]Column{}
	tables := map[uint32]bool{}
	previous := map[string]bool{}
	var risks []reporting.Risk
	for _, c := range after {
		now[key(c)] = c
		tables[c.TableOID] = true
	}
	dropped := map[uint32]bool{}
	for _, old := range before {
		previous[key(old)] = true
		n, ok := now[key(old)]
		add := func(code, msg string) { risks = append(risks, reporting.Risk{Code: code, Message: msg}) }
		if !tables[old.TableOID] {
			if !dropped[old.TableOID] {
				add("catalog_drop_table", "Observed catalog change: dropped table public."+old.Table)
				dropped[old.TableOID] = true
			}
			continue
		}
		if !ok {
			add("catalog_drop_column", "Observed catalog change: dropped column public."+old.Table+"."+old.Name)
			continue
		}
		if n.Name != old.Name {
			add("catalog_rename_column", fmt.Sprintf("Observed catalog change: renamed public.%s.%s to %s", old.Table, old.Name, n.Name))
		}
		if n.Type != old.Type {
			add("catalog_type_change", fmt.Sprintf("Observed catalog change: public.%s.%s type %s -> %s", old.Table, n.Name, old.Type, n.Type))
		}
		if !old.NotNull && n.NotNull {
			add("catalog_not_null", "Observed catalog change: added NOT NULL to public."+n.Table+"."+n.Name)
		}
	}
	for _, n := range after {
		if !previous[key(n)] && n.NotNull {
			risks = append(risks, reporting.Risk{Code: "catalog_not_null", Message: "Observed catalog change: added NOT NULL column public." + n.Table + "." + n.Name})
		}
	}
	return risks
}

var _ = strings.TrimSpace
