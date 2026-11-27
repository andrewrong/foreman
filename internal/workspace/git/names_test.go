package git

import "testing"

// state mints step ids as "<req id>/S<n>"; ValidateID (one segment) refuses
// them, so merge-back needs ValidateStepID.
func TestValidateStepID(t *testing.T) {
	for _, id := range []string{"r1/S1", "R-foreman-12/S2"} {
		if err := ValidateStepID(id); err != nil {
			t.Errorf("rejected %q", id)
		}
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID accepted %q", id)
		}
	}
	for _, id := range []string{"", "r1/", "/S1", "r1/S1/x", "r1/-S1", "r1/S 1", "a..b/S1"} {
		if err := ValidateStepID(id); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}

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
