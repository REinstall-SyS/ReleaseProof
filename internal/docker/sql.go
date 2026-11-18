package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SQL executes SQL inside our container with the scenario's non-superuser owner.
// psql's parser sends each statement separately, allowing INDEX CONCURRENTLY.
func (c *Client) SQL(ctx context.Context, container, database, user, password, sql string, timeout time.Duration) error {
	options := "-c statement_timeout=" + strconv.FormatInt(timeout.Milliseconds(), 10) + " -c lock_timeout=" + strconv.FormatInt(timeout.Milliseconds(), 10)
	var keyBytes [24]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		return fmt.Errorf("SQL restriction key: %w", err)
	}
	// Restricted mode is enforced by psql itself, independently of our lexical
	// checks. Only an unpredictable matching unrestrict command can leave it.
	restrict := "\\restrict " + hex.EncodeToString(keyBytes[:])
	args := []string{"exec", "--interactive", "--user", "postgres", "--env", "PGPASSWORD", "--env", "PGOPTIONS=" + options, container, "psql", "--no-psqlrc", "--host", "127.0.0.1", "--username", user, "--dbname", database, "--set", "ON_ERROR_STOP=1", "--set", "VERBOSITY=verbose", "--command", restrict, "--file=-"}
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
	cmd.Stdin = strings.NewReader(sql)
	var b limitedBuffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SQL execution failed: %w\n%s", err, b.String())
	}
	return nil
}
