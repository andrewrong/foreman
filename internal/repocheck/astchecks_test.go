package repocheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// importName returns the identifier under which importPath is visible in f
// ("." for dot imports). ok is false when it is not imported or imported as _.
func importName(f *ast.File, importPath string) (name string, ok bool) {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != importPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" {
				return "", false
			}
			return imp.Name.Name, true
		}
		return importPath[strings.LastIndex(importPath, "/")+1:], true
	}
	return "", false
}

// selectorCall matches a call to pkg.fn (or fn for a dot import) and returns it.
func isPkgCall(n ast.Node, pkgName, fn string) (*ast.CallExpr, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if id, ok := fun.X.(*ast.Ident); ok && pkgName != "." && id.Name == pkgName && fun.Sel.Name == fn {
			return call, true
		}
	case *ast.Ident:
		if pkgName == "." && fun.Name == fn {
			return call, true
		}
	}
	return nil, false
}

// eachGoFile parses every .go file under root (filter selects by relative
// path) and calls fn with the AST.
func eachGoFile(root string, filter func(rel string) bool, fn func(rel string, fset *token.FileSet, f *ast.File)) error {
	files, err := goFiles(root)
	if err != nil {
		return err
	}
	for _, rel := range files {
		if !filter(rel) {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		fn(rel, fset, f)
	}
	return nil
}

func isTestFile(rel string) bool { return strings.HasSuffix(rel, "_test.go") }

func anyGoFile(string) bool { return true }

// sleepViolations implements C-05: no time.Sleep in tests, and no time.Now()
// in tests outside internal/clock.
func sleepViolations(root string) ([]string, error) {
	var out []string
	err := eachGoFile(root, isTestFile, func(rel string, fset *token.FileSet, f *ast.File) {
		name, ok := importName(f, "time")
		if !ok {
			return
		}
		inClock := rel == "internal/clock" || strings.HasPrefix(rel, "internal/clock/")
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				// any reference to time.Sleep, not only calls
				if id, ok := sel.X.(*ast.Ident); ok && name != "." && id.Name == name && sel.Sel.Name == "Sleep" {
					out = append(out, fmt.Sprintf("%s: time.Sleep in test", fset.Position(sel.Pos())))
				}
			}
			if id, ok := n.(*ast.Ident); ok && name == "." && id.Name == "Sleep" {
				out = append(out, fmt.Sprintf("%s: time.Sleep (dot import) in test", fset.Position(id.Pos())))
			}
			if call, ok := isPkgCall(n, name, "Now"); ok && !inClock {
				out = append(out, fmt.Sprintf("%s: time.Now() in test outside internal/clock", fset.Position(call.Pos())))
			}
			return true
		})
	})
	sort.Strings(out)
	return out, err
}

// ghExecViolations implements check:no-gh-exec: no exec.Command or
// exec.CommandContext whose command name is the string literal "gh".
func ghExecViolations(root string) ([]string, error) {
	var out []string
	err := eachGoFile(root, anyGoFile, func(rel string, fset *token.FileSet, f *ast.File) {
		name, ok := importName(f, "os/exec")
		if !ok {
			return
		}
		ast.Inspect(f, func(n ast.Node) bool {
			for fn, idx := range map[string]int{"Command": 0, "CommandContext": 1} {
				call, ok := isPkgCall(n, name, fn)
				if !ok || len(call.Args) <= idx {
					continue
				}
				if lit, ok := call.Args[idx].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil && s == "gh" {
						out = append(out, fmt.Sprintf("%s: exec.%s of gh", fset.Position(call.Pos()), fn))
					}
				}
			}
			return true
		})
	})
	sort.Strings(out)
	return out, err
}

func TestNoSleepInTests(t *testing.T) {
	v, err := sleepViolations(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v {
		t.Error(s)
	}
}

func TestNoGhExec(t *testing.T) {
	v, err := ghExecViolations(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v {
		t.Error(s)
	}
}

func TestNoSleepInTestsDetectsViolations(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a/plain_test.go":          "package a\nimport \"time\"\nfunc f() { time.Sleep(1) }\n",
		"a/alias_test.go":          "package a\nimport tm \"time\"\nfunc g() { tm.Sleep(1) }\n",
		"a/dot_test.go":            "package a\nimport . \"time\"\nfunc h() { Sleep(1) }\n",
		"b/now_test.go":            "package b\nimport \"time\"\nfunc f() { _ = time.Now() }\n",
		"internal/clock/c_test.go": "package clock\nimport \"time\"\nfunc f() { _ = time.Now() }\n",
		"c/nontest.go":             "package c\nimport \"time\"\nfunc f() { time.Sleep(1); _ = time.Now() }\n",
		"c/other_test.go":          "package c\nimport \"time\"\nvar _ = time.Second\n",
	})
	v, err := sleepViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(v, "\n")
	for _, want := range []string{"plain_test.go:3", "alias_test.go:3", "dot_test.go:3", "b/now_test.go:3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if len(v) != 4 {
		t.Errorf("want exactly 4 violations (clock exempt, non-test and unrelated usage ignored), got %d:\n%s", len(v), joined)
	}
}

func TestNoGhExecDetectsViolations(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a/cmd.go":    "package a\nimport \"os/exec\"\nfunc f() { exec.Command(\"gh\", \"pr\") }\n",
		"a/ctx.go":    "package a\nimport (\"context\"; ex \"os/exec\")\nfunc f(c context.Context) { ex.CommandContext(c, \"gh\") }\n",
		"a/x_test.go": "package a\nimport \"os/exec\"\nfunc g() { exec.Command(\"gh\") }\n",
		"a/ok.go":     "package a\nimport \"os/exec\"\nfunc f() { exec.Command(\"git\", \"gh\"); exec.Command(name) }\nvar name = \"gh\"\n",
	})
	v, err := ghExecViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 {
		t.Fatalf("want 3 violations, got %d: %v", len(v), v)
	}
}
