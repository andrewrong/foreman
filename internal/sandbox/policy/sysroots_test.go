package policy

import (
	"strings"
	"testing"
)

// D-090 ⑥: the system roots are in denyRead whatever the inputs say, and a
// read path covering one is refused (it would re-open the whole area).
func TestPolicyAlwaysDeniesSystemRoots(t *testing.T) {
	roots := SystemDenyRoots()
	if len(roots) == 0 {
		t.Skip("no system roots on this machine")
	}
	in := base(t)
	in.DenyRoots = nil
	pol := mustNew(t, in)
	have := "\n" + strings.Join(pol.DenyReadPaths(), "\n") + "\n"
	for _, r := range roots {
		if !strings.Contains(have, "\n"+r+"\n") {
			t.Errorf("system root %s not in denyRead", r)
		}
	}
	in.ReadPaths = []string{roots[0]}
	if _, err := New(in); err == nil {
		t.Errorf("read path %s covering a system root accepted", roots[0])
	}
}
