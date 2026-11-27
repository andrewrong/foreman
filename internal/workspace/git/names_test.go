package git

import "testing"

func TestValidateName(t *testing.T) {
	good := []string{"main", "foreman/r1", "step/s-1.a", "refs/foreman/steps/x"}
	bad := []string{"", "-x", "/x", "a..b", "a//b", "a/", "a.lock", "a/.hidden", "a b", "a;b", "x\n", "a@{1}"}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("rejected %q", n)
		}
	}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("accepted %q", n)
		}
	}
	if _, err := RequirementBranch("a/b"); err == nil {
		t.Error("req id with slash accepted")
	}
	if b, err := RequirementBranch("r1"); err != nil || b != "foreman/r1" {
		t.Errorf("got %q %v", b, err)
	}
}
