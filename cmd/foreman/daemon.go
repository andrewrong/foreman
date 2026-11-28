package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/config/schema"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/harness/claude"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/secrets"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
)

// githubTokenName is the secret file foreman.github.token in secrets_dir (D-026).
const githubTokenName = "github.token"

// configFromFiles maps the validated configuration onto Compose's Config.
// Every value comes from the files: nothing is defaulted here (D-074). The
// daemon serves one project; zero or several project files are refused.
func configFromFiles(home, ownerHome string, eff *schema.Effective) (Config, error) {
	names := make([]string, 0, len(eff.Projects))
	for n := range eff.Projects {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) != 1 {
		return Config{}, fmt.Errorf("config/%s: want exactly one project file, found %d (%s)", schema.ProjectsDir, len(names), strings.Join(names, ", "))
	}
	p := eff.Projects[names[0]]
	owner, repo, ok := strings.Cut(p.GitHubRepo, "/")
	if !ok || owner == "" || repo == "" {
		return Config{}, fmt.Errorf("config/%s/%s.toml: github_repo %q is not owner/name", schema.ProjectsDir, p.Name, p.GitHubRepo)
	}
	rt := eff.Runtime
	return Config{
		Home: home, Project: p.Name,
		// The mirror clones the GitHub remote; repo_path is the owner's own
		// checkout, which foremand never touches (D-050).
		Remote: "https://github.com/" + p.GitHubRepo + ".git", Repo: ops.Repo{Owner: owner, Name: repo},
		RepoPath: p.RepoPath, DefaultBranch: p.DefaultBranch,
		Clock: clock.Real(), Harness: "claude", Models: eff.Models(),
		SrtPath: rt.Sandbox.SrtPath, NodeDir: rt.Sandbox.NodeDir,
		ReadPaths: rt.Sandbox.ReadPaths, DenyRoots: rt.Sandbox.DenyRoots, OwnerHome: ownerHome,
		SessionGit: wsgit.Identity{Name: rt.SessionGit.Name, Email: rt.SessionGit.Email},
		SecretsDir: rt.SecretsDir, Workers: rt.Parallel.Workers,
		Path: wsgit.DefaultPath, L0: p.L0, Ledger: ledger.New(),
		Toolchain: launch.Mise{EmptyConfig: filepath.Join(home, "githome", "mise", "config.toml")},
	}, nil
}

// runDaemon is `foreman daemon`: it loads and validates the configuration,
// checks that the harness is on PATH, and only then touches the database and
// the network (Compose). Any missing owner key refuses the start with one line
// per key (D-074). Returns the exit code.
func runDaemon(ctx context.Context, home string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		for _, l := range strings.Split(strings.TrimSpace(err.Error()), "\n") {
			fmt.Fprintf(stderr, "foreman daemon: %s\n", strings.TrimSpace(l))
		}
		return 1
	}
	if home == "" {
		return fail(errors.New("FOREMAN_HOME is not set"))
	}
	eff, err := schema.Load(home)
	if err != nil {
		return fail(err)
	}
	ownerHome, err := os.UserHomeDir()
	if err != nil {
		return fail(err)
	}
	cfg, err := configFromFiles(home, ownerHome, eff)
	if err != nil {
		return fail(err)
	}
	if _, err := claude.Locate(); err != nil { // D-084
		return fail(err)
	}
	tok, err := secrets.Read(cfg.SecretsDir, githubTokenName, secrets.Token)
	if err != nil {
		return fail(err)
	}
	cfg.GitHubToken = tok.Reveal()
	app, err := Compose(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	defer app.Close()
	for _, c := range app.Startup {
		fmt.Fprintln(stdout, c)
	}
	return 0
}
