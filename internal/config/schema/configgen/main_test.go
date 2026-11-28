package main

import (
	"bytes"
	"os"
	"testing"
)

func TestConfigDefaultsGenerated(t *testing.T) {
	yamlData, err := os.ReadFile("../../../../spec/design/config.yaml")
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
	got, err := os.ReadFile("../defaults_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("internal/config/schema/defaults_gen.go is stale: run `go generate ./internal/config/schema`")
	}
}
