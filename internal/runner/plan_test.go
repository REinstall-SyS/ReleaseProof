package runner

import (
	"reflect"
	"testing"

	"github.com/REinstall-SyS/ReleaseProof/internal/config"
	"github.com/REinstall-SyS/ReleaseProof/internal/reporting"
)

func TestPlanRunsBaselinesFirstAndKeepsScenarioOrder(t *testing.T) {
	c := &config.Config{Scenarios: []config.Scenario{{ID: "old-after", Kind: "old_after"}, {ID: "baseline-a", Kind: "baseline"}, {ID: "new-after", Kind: "new_after"}, {ID: "baseline-b", Kind: "baseline"}, {ID: "old-to-new", Kind: "old_to_new"}}}
	plan, err := Plan(c, "")
	if err != nil {
		t.Fatal(err)
	}
	if actual := scenarioIDs(plan); !reflect.DeepEqual(actual, []string{"baseline-a", "baseline-b", "old-after", "new-after", "old-to-new"}) {
		t.Fatalf("unexpected plan: %v", actual)
	}
	if c.Scenarios[0].ID != "old-after" {
		t.Fatal("planning mutated input configuration")
	}
}

func TestPlanSelectedScenarioRetainsAllBaselineChecks(t *testing.T) {
	c := &config.Config{Scenarios: []config.Scenario{{ID: "old-after", Kind: "old_after"}, {ID: "baseline-a", Kind: "baseline"}, {ID: "new-after", Kind: "new_after"}, {ID: "baseline-b", Kind: "baseline"}}}
	for _, tc := range []struct {
		selected string
		want     []string
	}{{"old-after", []string{"baseline-a", "baseline-b", "old-after"}}, {"baseline-b", []string{"baseline-a", "baseline-b"}}} {
		t.Run(tc.selected, func(t *testing.T) {
			plan, err := Plan(c, tc.selected)
			if err != nil || !reflect.DeepEqual(scenarioIDs(plan), tc.want) {
				t.Fatalf("selected plan=%v err=%v, want %v", scenarioIDs(plan), err, tc.want)
			}
		})
	}
	if _, err := Plan(c, "missing"); err == nil {
		t.Fatal("unknown scenario ID was accepted")
	}
}

func TestVerdictPrioritizesExecutionAndBaselineFailures(t *testing.T) {
	base := reporting.Scenario{ID: "baseline", Kind: "baseline", Status: "pass"}
	for _, tc := range []struct {
		name      string
		scenarios []reporting.Scenario
		want      string
	}{
		{"all-pass", []reporting.Scenario{base, {Kind: "old_after", Status: "pass"}}, "compatible"},
		{"compatibility-regression", []reporting.Scenario{base, {Kind: "old_after", Status: "fail"}}, "incompatible"},
		{"execution-error-over-failure", []reporting.Scenario{base, {Kind: "old_after", Status: "fail"}, {Kind: "new_after", Status: "error"}}, "error"},
		{"baseline-assertion-failure", []reporting.Scenario{{Kind: "baseline", Status: "fail"}, {Kind: "old_after", Status: "skipped"}}, "baseline_failed"},
		{"baseline-infrastructure-failure", []reporting.Scenario{{Kind: "baseline", Status: "error"}, {Kind: "old_after", Status: "skipped"}}, "error"},
		{"unsupported-is-not-compatible", []reporting.Scenario{base, {Kind: "old_after", Status: "unsupported"}}, "error"},
		{"skipped-after-passing-baseline", []reporting.Scenario{base, {Kind: "old_after", Status: "skipped"}}, "error"},
		{"no-evidence", nil, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if actual := Verdict(tc.scenarios); actual != tc.want {
				t.Fatalf("verdict=%s, want %s", actual, tc.want)
			}
		})
	}
}

func scenarioIDs(scenarios []config.Scenario) []string {
	ids := make([]string, len(scenarios))
	for i, scenario := range scenarios {
		ids[i] = scenario.ID
	}
	return ids
}
