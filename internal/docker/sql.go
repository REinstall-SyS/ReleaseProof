package docker

import (
	"context"
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
	args := []string{"exec", "--interactive", "--env", "PGPASSWORD", "--env", "PGOPTIONS=" + options, container, "psql", "--no-psqlrc", "--host", "127.0.0.1", "--username", user, "--dbname", database, "--set", "ON_ERROR_STOP=1", "--set", "VERBOSITY=verbose", "--file=-"}
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
