package execplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateMatchesRepositoryPlan(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "PLAN.md"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := Template(), strings.TrimSpace(string(data)); got != want {
		t.Fatal("embedded exec plan template drifted from repository PLAN.md")
	}
}
