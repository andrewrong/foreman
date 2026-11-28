package state

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"go.yaml.in/yaml/v3"
)

// catalogNames reads the effects catalog of state-machine.yaml.
func catalogNames(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRootDir(t), "spec", "design", "state-machine.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Effects []struct {
			Name   string `yaml:"name"`
			Writes string `yaml:"writes"`
		} `yaml:"effects"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Effects) == 0 {
		t.Fatal("no effects catalog in state-machine.yaml")
	}
	var out []string
	for _, e := range doc.Effects {
		if e.Writes == "" {
			t.Errorf("catalog effect %q has no writes", e.Name)
		}
		out = append(out, catalogKey(e.Name))
	}
	return out
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// TestEffectsAllExecuted: every effect of the generated table has an
// executor, and the executors are exactly the catalog's effects.
func TestEffectsAllExecuted(t *testing.T) {
	exec := executors()

	var used []string
	for _, r := range stepRows {
		used = append(used, r.Effects...)
	}
	for _, r := range escRows {
		used = append(used, r.Effects...)
	}
	for _, r := range holdRows {
		used = append(used, r.Effects...)
	}
	for _, r := range holdEscRows {
		used = append(used, r.Effects...)
	}
	if len(used) == 0 {
		t.Fatal("generated table has no effects")
	}
	for _, eff := range used {
		if _, _, ok := resolveEffect(exec, eff); !ok {
			t.Errorf("table effect %q has no executor", eff)
		}
	}

	catalog := catalogNames(t)
	sort.Strings(catalog)
	var have []string
	for k := range exec {
		have = append(have, k)
	}
	sort.Strings(have)
	if !slices.Equal(catalog, have) {
		t.Errorf("executors %v\n!= catalog     %v", have, catalog)
	}
}
