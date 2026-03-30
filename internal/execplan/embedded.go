package execplan

import (
	_ "embed"
	"strings"
)

// Template is the embedded ExecPlan authoring guide used for plan-generation turns.
//
// Keep this file in sync with the repository root PLAN.md.
//
//go:embed PLAN.md
var template string

// Template returns the trimmed embedded plan template text.
func Template() string {
	return strings.TrimSpace(template)
}
