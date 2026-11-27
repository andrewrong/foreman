package main

import (
	"bytes"
	"os"
	"testing"
)

func TestGeneratedTableUpToDate(t *testing.T) {
	yamlData, err := os.ReadFile("../../../../spec/design/state-machine.yaml")
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
	got, err := os.ReadFile("../table_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("internal/pipeline/state/table_gen.go is stale: run `go generate ./internal/pipeline/state`")
	}
}
