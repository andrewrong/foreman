package smcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSpec(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "design", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStateTableExhaustive(t *testing.T) {
	res, err := Check([]byte(readSpec(t, "state-machine.yaml")), []byte(readSpec(t, "protocol-run.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	t.Log(res.Summary())
	if len(res.Errors) != 0 {
		t.Fatalf("%d errors:\n%s", len(res.Errors), strings.Join(res.Errors, "\n"))
	}
	if res.Configs != 101 || res.Events != 33 || res.Rows != 77 || res.Expanded != 84 {
		t.Fatalf("counts differ from the ruby tool: %s", res.Summary())
	}
}
