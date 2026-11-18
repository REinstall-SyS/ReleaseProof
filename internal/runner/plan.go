package runner

import (
	"fmt"
	"github.com/REinstall-SyS/ReleaseProof/internal/config"
)

// Plan always includes all baseline checks before migrated scenarios, including
// when reproducing a single scenario. A broken baseline cannot implicate a DDL.
func Plan(c *config.Config, selected string) ([]config.Scenario, error) {
	found := selected == ""
	var baseline, other []config.Scenario
	for _, s := range c.Scenarios {
		if s.ID == selected {
			found = true
		}
		if s.Kind == "baseline" {
			baseline = append(baseline, s)
		} else if selected == "" || s.ID == selected {
			other = append(other, s)
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown scenario ID %q", selected)
	}
	return append(baseline, other...), nil
}
