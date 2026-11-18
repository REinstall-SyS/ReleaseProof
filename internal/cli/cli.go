// Package cli defines ReleaseProof's public commands and exit-code contract.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/REinstall-SyS/ReleaseProof/internal/config"
	"github.com/REinstall-SyS/ReleaseProof/internal/reporting"
	"github.com/REinstall-SyS/ReleaseProof/internal/runner"
	"github.com/spf13/cobra"
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return "check did not pass"
}
func (e *exitError) Unwrap() error { return e.err }
func Execute(ctx context.Context, args []string, out, stderr io.Writer, version string) int {
	root := New(out, stderr, version)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		var exit *exitError
		if errors.As(err, &exit) {
			if exit.err != nil {
				fmt.Fprintln(stderr, exit.err)
			}
			return exit.code
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}
func New(out, stderr io.Writer, version string) *cobra.Command {
	root := &cobra.Command{Use: "releaseproof", Short: "Catch database migrations that break running applications before deployment.", Version: version, SilenceUsage: true, SilenceErrors: true}
	root.SetOut(out)
	root.SetErr(stderr)
	var configPath, output, validatePath string
	check := &cobra.Command{Use: "check", Short: "Execute the baseline and cross-version compatibility matrix", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := config.Load(configPath)
		if err != nil {
			return &exitError{2, err}
		}
		dir, err := outputPath(c, output)
		if err != nil {
			return &exitError{2, err}
		}
		return run(cmd.Context(), c, runner.Options{Output: dir, Observe: func(s string) { fmt.Fprintln(stderr, s) }}, out)
	}}
	check.Flags().StringVar(&configPath, "config", "releaseproof.yaml", "Configuration file (input paths resolve relative to it)")
	check.Flags().StringVar(&output, "output", "", "Report root (default: config report, relative to configuration)")
	var reportPath, reproConfig, reproOutput string
	var allowChanged bool
	reproduce := &cobra.Command{Use: "reproduce <scenario-id>", Short: "Rerun a scenario and its baseline using recorded local inputs", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		metadata, err := reporting.Read(reportPath)
		if err != nil {
			return &exitError{2, err}
		}
		path := metadata.Config
		if reproConfig != "" {
			path = reproConfig
		}
		c, err := config.Load(path)
		if err != nil {
			return &exitError{2, err}
		}
		fingerprint, err := c.Fingerprint()
		if err != nil {
			return &exitError{2, err}
		}
		if _, err := runner.Plan(c, args[0]); err != nil {
			return &exitError{2, err}
		}
		recorded := false
		for _, s := range metadata.Scenarios {
			if s.ID == args[0] {
				recorded = true
			}
		}
		if !recorded {
			return &exitError{2, fmt.Errorf("scenario %q is not present in this report", args[0])}
		}
		if !allowChanged && fingerprint != metadata.Fingerprint {
			return &exitError{2, fmt.Errorf("recorded inputs changed: restore the recorded configuration/SQL/build sources or explicitly pass --allow-changed for a fresh experiment")}
		}
		expected := metadata.Images
		if allowChanged {
			expected = nil
		}
		dir, err := outputPath(c, reproOutput)
		if err != nil {
			return &exitError{2, err}
		}
		return run(cmd.Context(), c, runner.Options{Selected: args[0], Output: dir, ExpectedImages: expected, Observe: func(s string) { fmt.Fprintln(stderr, s) }}, out)
	}}
	reproduce.Flags().StringVar(&reportPath, "report", "", "Path to an existing report.json (required)")
	_ = reproduce.MarkFlagRequired("report")
	reproduce.Flags().StringVar(&reproConfig, "config", "", "Config location override after moving a checkout")
	reproduce.Flags().StringVar(&reproOutput, "output", "", "Report root for the reproduction")
	reproduce.Flags().BoolVar(&allowChanged, "allow-changed", false, "Explicitly run changed source/images; this is a fresh experiment")
	validate := &cobra.Command{Use: "validate", Short: "Validate configuration and inputs without requiring Docker", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		c, err := config.Load(validatePath)
		if err != nil {
			return &exitError{2, err}
		}
		fingerprint, err := c.Fingerprint()
		if err != nil {
			return &exitError{2, err}
		}
		fmt.Fprintf(out, "Configuration valid: %s\nInput fingerprint: %s\n", c.Path, fingerprint)
		return nil
	}}
	validate.Flags().StringVar(&validatePath, "config", "releaseproof.yaml", "Configuration file")
	root.AddCommand(check, reproduce, validate)
	return root
}
func outputPath(c *config.Config, override string) (string, error) {
	if override == "" {
		return filepath.Abs(c.Resolve(c.Report))
	}
	return filepath.Abs(override)
}
func run(ctx context.Context, c *config.Config, options runner.Options, out io.Writer) error {
	r := runner.Run(ctx, c, options)
	path, err := reporting.Write(filepath.Join(options.Output, r.RunID), r)
	reporting.Terminal(out, r)
	if err != nil {
		return &exitError{3, err}
	}
	fmt.Fprintf(out, "Reports: %s\n", path)
	if code := reporting.ExitCode(r); code != 0 {
		return &exitError{code, nil}
	}
	return nil
}
