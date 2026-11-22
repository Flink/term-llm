package legacyconfig

import (
	"testing"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

func TestProviderLabelMigrationMatchesCurrentProfiles(t *testing.T) {
	for id, change := range providerLabelChangesV11 {
		if p, ok := proxyprofile.ByID(id); !ok || p.Label != change.new {
			t.Errorf("migrated label for %s is %q, current default is %q", id, change.new, p.Label)
		}
	}
}
