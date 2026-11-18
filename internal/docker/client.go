// Package docker uses the Docker CLI with argument arrays, never a host shell.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/REinstall-SyS/ReleaseProof/internal/config"
)

const Label = "io.releaseproof.run"

type Client struct {
	Binary     string
	ID         string
	Network    string
	containers []string
	images     []string
}
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 64*1024 - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
func (c *Client) command(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errout limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return out.String(), ctx.Err()
		}
		return out.String(), fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(errout.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
func New(ctx context.Context, id string) (*Client, error) {
	binary, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("Docker CLI is required: install Docker Engine or Docker Desktop with Linux containers")
	}
	c := &Client{Binary: binary, ID: id, Network: "releaseproof-" + id}
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" {
		endpoint, err = c.command(ctx, nil, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		if err != nil {
			return nil, err
		}
	}
	if !strings.HasPrefix(endpoint, "unix://") && !strings.HasPrefix(endpoint, "npipe://") {
		return nil, fmt.Errorf("only a local Docker daemon (unix:// or npipe://) is supported; remote port forwarding is not supported")
	}
	kind, err := c.command(ctx, nil, "info", "--format", "{{.OSType}}")
	if err != nil {
		return nil, fmt.Errorf("Docker daemon unavailable: %w", err)
	}
	if kind != "linux" {
		return nil, fmt.Errorf("Linux containers required; Docker reported %q", kind)
	}
	return c, nil
}
func (c *Client) CreateNetwork(ctx context.Context) error {
	_, err := c.command(ctx, nil, "network", "create", "--label", Label+"="+c.ID, c.Network)
	return err
}
func (c *Client) PrepareImage(ctx context.Context, name string, app config.App, resolve func(string) string) (string, string, error) {
	image := app.Image
	if app.Build != "" {
		image = "releaseproof-" + c.ID + "-" + name
		c.images = append(c.images, image)
		args := []string{"build", "--tag", image}
		if app.Dockerfile != "" {
			args = append(args, "--file", resolve(app.Build+"/"+app.Dockerfile))
		}
		args = append(args, resolve(app.Build))
		if _, err := c.command(ctx, nil, args...); err != nil {
			return "", "", err
		}
	} else if _, err := c.command(ctx, nil, "image", "inspect", image); err != nil {
		if _, err = c.command(ctx, nil, "pull", image); err != nil {
			return "", "", err
		}
	}
	digest, err := c.command(ctx, nil, "image", "inspect", "--format", "{{.Id}}", image)
	return image, digest, err
}
func (c *Client) CreateContainer(ctx context.Context, name, image string, port int, env map[string]string, command []string, database bool) (string, error) {
	// Register before create: cancellation can race the daemon accepting a request.
	c.containers = append(c.containers, name)
	args := []string{"create", "--name", name, "--label", Label + "=" + c.ID, "--network", c.Network, "--publish", "127.0.0.1::" + strconv.Itoa(port), "--log-opt", "max-size=1m", "--log-opt", "max-file=1"}
	if database {
		args = append(args, "--network-alias", "postgres")
	} else {
		args = append(args, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "256", "--memory", "512m", "--cpus", "1")
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	processEnv := make([]string, 0, len(env))
	for _, key := range keys {
		args = append(args, "--env", key)
		processEnv = append(processEnv, key+"="+env[key])
	}
	args = append(args, image)
	args = append(args, command...)
	if _, err := c.command(ctx, processEnv, args...); err != nil {
		return "", err
	}
	if _, err := c.command(ctx, nil, "start", name); err != nil {
		return "", err
	}
	return c.Port(ctx, name, port)
}
func (c *Client) Port(ctx context.Context, name string, port int) (string, error) {
	output, err := c.command(ctx, nil, "port", name, strconv.Itoa(port)+"/tcp")
	if err != nil {
		return "", err
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(output))
	if err != nil || host != "127.0.0.1" {
		return "", fmt.Errorf("expected one loopback port mapping, got %q", output)
	}
	return output, nil
}
func (c *Client) Running(ctx context.Context, name string) bool {
	s, err := c.command(ctx, nil, "inspect", "--format", "{{.State.Running}}", name)
	return err == nil && s == "true"
}
func (c *Client) Logs(ctx context.Context, name string) string {
	s, err := c.command(ctx, nil, "logs", "--tail", "60", name)
	if err != nil {
		return err.Error()
	}
	return s
}

// Logs needs stdout AND stderr because most application loggers use stderr.
func (c *Client) CombinedLogs(ctx context.Context, name string) string {
	cmd := exec.CommandContext(ctx, c.Binary, "logs", "--tail", "60", name)
	var b limitedBuffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	_ = cmd.Run()
	return b.String()
}
func (c *Client) Remove(ctx context.Context, names ...string) error {
	var errs []error
	for _, name := range names {
		owned, err := c.command(ctx, nil, "inspect", "--format", `{{index .Config.Labels "`+Label+`"}}`, name)
		if err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "no such") {
				errs = append(errs, err)
			}
			continue
		}
		if owned != c.ID {
			errs = append(errs, fmt.Errorf("refusing to remove container without this run's ownership label: %s", name))
			continue
		}
		if _, err := c.command(ctx, nil, "rm", "--force", "--volumes", name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func (c *Client) Cleanup(ctx context.Context) error {
	var errs []error
	if err := c.Remove(ctx, c.containers...); err != nil {
		errs = append(errs, err)
	}
	owned, err := c.command(ctx, nil, "network", "inspect", "--format", `{{index .Labels "`+Label+`"}}`, c.Network)
	if err == nil && owned == c.ID {
		if _, err = c.command(ctx, nil, "network", "rm", c.Network); err != nil {
			errs = append(errs, err)
		}
	} else if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such") {
		errs = append(errs, err)
	}
	for _, image := range c.images {
		if _, err := c.command(ctx, nil, "image", "rm", image); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// OwnedResources is used by integration tests to verify cleanup without deleting
// resources from other runs. It performs no mutations.
func OwnedResources(ctx context.Context, id string) ([]string, error) {
	binary, err := exec.LookPath("docker")
	if err != nil {
		return nil, err
	}
	c := Client{Binary: binary}
	output, err := c.command(ctx, nil, "ps", "--all", "--quiet", "--filter", "label="+Label+"="+id)
	if err != nil {
		return nil, err
	}
	return strings.Fields(output), nil
}

// Keep imports tied to compile-time supported interfaces; no Docker socket is
// mounted in application containers.
var _ io.Writer = (*limitedBuffer)(nil)
