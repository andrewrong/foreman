package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// SessionGit is the content of the generated session .gitconfig (D-082).
type SessionGit struct {
	Name          string // session_git.name
	Email         string // session_git.email (a noreply address)
	DefaultBranch string // init.defaultBranch: the project's default branch
}

func checkSessionGit(g SessionGit) error {
	for name, v := range map[string]string{"Name": g.Name, "Email": g.Email, "DefaultBranch": g.DefaultBranch} {
		if v == "" {
			return fmt.Errorf("policy: SessionGit.%s is required (D-082)", name)
		}
		if strings.ContainsAny(v, "\n\r\x00") || strings.TrimSpace(v) != v {
			return fmt.Errorf("policy: SessionGit.%s has a control character or surrounding space", name)
		}
	}
	if !strings.Contains(g.Email, "@") {
		return errors.New("policy: SessionGit.Email is not an address")
	}
	return nil
}

// quoteValue renders a config value in double quotes.
func quoteValue(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

// GitConfigPath is the generated global config file in the run HOME.
func (p Policy) GitConfigPath() string { return filepath.Join(p.Home(), ".gitconfig") }

// GitConfig returns the exact D-082 content: the identity and the default
// branch, no signing, no editor or pager, and push.default=nothing because a
// session never pushes (only foremand does).
func (p Policy) GitConfig() []byte {
	g := p.in.SessionGit
	var b strings.Builder
	b.WriteString("[user]\n\tname = " + quoteValue(g.Name) + "\n\temail = " + quoteValue(g.Email) + "\n")
	b.WriteString("[init]\n\tdefaultBranch = " + quoteValue(g.DefaultBranch) + "\n")
	b.WriteString("[commit]\n\tgpgsign = false\n")
	b.WriteString("[core]\n\teditor = true\n\tpager = cat\n")
	b.WriteString("[advice]\n\tdetachedHead = false\n")
	b.WriteString("[push]\n\tdefault = nothing\n")
	return []byte(b.String())
}
