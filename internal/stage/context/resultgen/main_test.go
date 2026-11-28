package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestResultDocGenerated(t *testing.T) {
	yamlData, err := os.ReadFile("../../../../spec/design/protocol-run.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := generate(yamlData)
	if err != nil {
		t.Fatal(err)
	}
	again, err := generate(yamlData)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, again) {
		t.Fatal("generator output is not deterministic")
	}
	got, err := os.ReadFile("../resultdoc_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("internal/stage/context/resultdoc_gen.go is stale: run `go generate ./internal/stage/context`")
	}
	if strings.Contains(string(want), "invalid_reviewer_verdict") || strings.Contains(string(want), `"approve", "findings"`) {
		t.Error("an example marked invalid reached the generated file")
	}
}

func TestResultDocGeneratorRejectsSpecWithoutResult(t *testing.T) {
	if _, err := generate([]byte("files:\n  - id: PF-CTX\n    fields: []\n")); err == nil {
		t.Error("spec without PF-RESULT accepted")
	}
}
