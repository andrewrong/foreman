package main

import (
	"bytes"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"version"}, 0, "foreman dev\n", ""},
		{"no args", nil, 2, "", "usage: foreman version | daemon\n"},
		{"unknown", []string{"bogus"}, 2, "", "usage: foreman version | daemon\n"},
		{"version extra arg", []string{"version", "x"}, 2, "", "usage: foreman version | daemon\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(tt.args, &out, &errb)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if out.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantStdout)
			}
			if errb.String() != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", errb.String(), tt.wantStderr)
			}
		})
	}
}
