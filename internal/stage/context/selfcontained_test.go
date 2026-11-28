package context

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/andrewrong/foreman/internal/harness/api"
)

// resultFieldNames reads the field names of PF-RESULT from the spec.
func resultFieldNames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/design/protocol-run.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Files []struct {
			ID     string `yaml:"id"`
			Fields []struct {
				Name string `yaml:"name"`
			} `yaml:"fields"`
		} `yaml:"files"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range spec.Files {
		if f.ID != "PF-RESULT" {
			continue
		}
		for _, fl := range f.Fields {
			names = append(names, fl.Name)
		}
	}
	if len(names) == 0 {
		t.Fatal("PF-RESULT has no fields")
	}
	return names
}

var (
	fileRef   = regexp.MustCompile(`[A-Za-z0-9_./-]+\.(?:md|yaml|json|toml|txt)\b`)
	jsonBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")
)

// checkSelfContained judges the context.md and prompt of one role.
func checkSelfContained(t *testing.T, role, runID, prompt string, fieldNames []string) {
	t.Helper()
	in := sample()
	in.Role, in.RunID, in.StepID, in.Diff = role, runID, "R-demo-7/S3", "+x"
	out := Render(in)

	if !strings.Contains(out, "`run_id` = `"+runID+"`") || !strings.Contains(out, "`role` = `"+role+"`") {
		t.Errorf("%s: context.md does not state run_id and role", role)
	}
	for _, name := range fieldNames {
		if !strings.Contains(out, "`"+name+"`") {
			t.Errorf("%s: result.json field %s not described in context.md", role, name)
		}
	}
	m := jsonBlock.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("%s: no result.json example in context.md", role)
	}
	if _, err := api.ValidateResult([]byte(m[1]), api.ResultContext{RunID: runID, Role: api.Role(role),
		Scope: "step", AcceptanceIDs: []string{"A3"}}); err != nil {
		t.Errorf("%s: the example in context.md is rejected by the validator: %v\n%s", role, err, m[1])
	}
	readable := map[string]bool{"context.md": true, "result.json": true}
	for _, text := range []string{prompt, out} {
		for _, ref := range fileRef.FindAllString(text, -1) {
			if !readable[ref] {
				t.Errorf("%s: refers to %s, which the session cannot read", role, ref)
			}
		}
	}
}

// D-091: a session can do its job from the run directory alone. context.md
// states the run_id and role, describes every result.json field of
// protocol-run.yaml PF-RESULT and carries an example that foremand itself
// accepts; neither context.md nor a prompt refers to a file the session
// cannot read.
func TestSessionContextSelfContained(t *testing.T) {
	names := resultFieldNames(t)
	checkSelfContained(t, "coder", "R-demo-7/S3/r2", CoderPrompt, names)
	checkSelfContained(t, "reviewer", "R-demo-7/S3/r2", ReviewerPrompt, names)
}
