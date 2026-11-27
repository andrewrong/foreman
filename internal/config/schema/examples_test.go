package schema

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigExamplesAreValid(t *testing.T) {
	src := filepath.Join("..", "..", "..", "config", "examples")
	home := t.TempDir()
	for _, rel := range []string{RuntimeFile, RolesFile, "projects/example.toml"} {
		b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		write(t, home, rel, string(b))
	}
	e, err := Load(home)
	if err != nil {
		t.Fatalf("examples do not load: %v", err)
	}
	if e.Projects["example"].RepoPath == "" || e.Runtime.SecretsDir == "" {
		t.Errorf("examples not applied: %+v", e)
	}
}
