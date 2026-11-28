package privguard

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestPrivguardCommitMetadata(t *testing.T) {
	good := Commit{ID: "c1", AuthorName: "foreman", AuthorEmail: "foreman@users.noreply.example",
		CommitterName: "foreman", CommitterEmail: "12345+bot@users.noreply.github.com",
		Message: "Add the thing\n\nBody text."}
	if fs := CheckCommit(good); len(fs) != 0 {
		t.Fatalf("clean commit rejected: %v", fs)
	}

	badAuthor := good
	badAuthor.AuthorEmail = email()
	if fs := CheckCommit(badAuthor); len(fs) != 1 || fs[0].Kind != "author-email" || fs[0].Rule != RuleCommit {
		t.Fatalf("author: %v", fs)
	}
	// example.* placeholder domains are fine for P2 text but are not noreply
	// addresses, so P4 still rejects them.
	placeholder := good
	placeholder.CommitterEmail = "dev@" + "example.com"
	if fs := CheckCommit(placeholder); len(fs) != 1 || fs[0].Kind != "committer-email" {
		t.Fatalf("committer: %v", fs)
	}
	noAt := good
	noAt.AuthorEmail = ""
	if fs := CheckCommit(noAt); len(fs) != 1 {
		t.Fatalf("empty email: %v", fs)
	}

	msg := good
	msg.Message = "fix in " + homePath() + "\n\ntoken " + ghToken()
	fs := CheckCommit(msg)
	if len(fs) != 2 || fs[0].Kind != "abs-path" || fs[1].Kind != "token" {
		t.Fatalf("message: %v", fs)
	}

	sc, err := New(Config{Literals: []string{"zed42"}})
	if err != nil {
		t.Fatal(err)
	}
	named := good
	named.AuthorName = "Zed42"
	if fs := sc.CheckCommit(named); len(fs) != 1 || fs[0].Kind != "name-owner-literal" {
		t.Fatalf("name: %v", fs)
	}
}

func gitIn(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(ScrubbedGitEnv(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestPrivguardCommitMetadataRange(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, nil, "init", "-q", "-b", "main")
	commit := func(msg, author, committer string) {
		gitIn(t, dir, []string{
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=" + author,
			"GIT_COMMITTER_NAME=c", "GIT_COMMITTER_EMAIL=" + committer,
		}, "commit", "-q", "--allow-empty", "-m", msg)
	}
	noreply := "bot@users.noreply.example"
	commit("base", noreply, noreply)
	gitIn(t, dir, nil, "tag", "base")
	commit("ok", noreply, noreply)
	commit("leaks "+homePath(), noreply, noreply)
	commit("wrong author", email(), noreply)

	sc, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := sc.CheckRange(context.Background(), dir, "base..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(kinds(fs), ","); got != "author-email,abs-path" {
		t.Fatalf("findings %q (%v)", got, fs)
	}
	commits, err := CommitsInRange(context.Background(), dir, "base..HEAD")
	if err != nil || len(commits) != 3 || commits[2].Message != "ok" {
		t.Fatalf("commits=%v err=%v", commits, err)
	}
	if fs, err := sc.CheckRange(context.Background(), dir, "base..base"); err != nil || len(fs) != 0 {
		t.Fatalf("empty range: %v %v", fs, err)
	}
	if _, err := sc.CheckRange(context.Background(), dir, "nosuchref..HEAD"); err == nil {
		t.Fatal("bad range accepted")
	}
}
