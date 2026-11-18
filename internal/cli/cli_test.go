package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIConfigurationExitCodes(t *testing.T) {
	for _, args := range [][]string{{"check", "--config", "does-not-exist.yaml"}, {"reproduce", "unknown"}, {"check", "--bogus"}, {"check", "unexpected"}} {
		var out, err bytes.Buffer
		if code := Execute(context.Background(), args, &out, &err, "test"); code != 2 {
			t.Fatalf("%v: exit%d %s %s", args, code, &out, &err)
		}
	}
}
func TestHelpDescribesProduct(t *testing.T) {
	var out, err bytes.Buffer
	code := Execute(context.Background(), []string{"--help"}, &out, &err, "test")
	if code != 0 || !strings.Contains(out.String(), "database migrations") || !strings.Contains(out.String(), "reproduce") {
		t.Fatalf("help exit%d: %s %s", code, &out, &err)
	}
}
func TestValidateDoesNotNeedDocker(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	config := filepath.Join(filepath.Dir(file), "..", "..", "examples", "rolling-deployment", "safe.yaml")
	var out, err bytes.Buffer
	code := Execute(context.Background(), []string{"validate", "--config", config}, &out, &err, "test")
	if code != 0 || !strings.Contains(out.String(), "Configuration valid") {
		t.Fatalf("validate exit%d: %s %s", code, &out, &err)
	}
}
