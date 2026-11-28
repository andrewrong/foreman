package repocheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// listedPkg is the subset of `go list -json` output the dependency checks use.
type listedPkg struct {
	Dir          string
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

// goList runs `go list -e -json ./...` in root and returns the packages found,
// keyed by module-relative directory (slash-separated). -e keeps packages whose
// imports do not resolve (fixtures import packages that do not exist).
func goList(root string) (map[string]listedPkg, error) {
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	cmd := exec.Command("go", "list", "-e", "-json=Dir,ImportPath,Imports,TestImports,XTestImports", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, stderr.String())
	}
	res := map[string]listedPkg{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p listedPkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("go list: decode: %w", err)
		}
		rel, err := filepath.Rel(root, p.Dir)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if inPrefix(rel, "internal") || inPrefix(rel, "cmd") {
			res[rel] = p
		}
	}
	return res, nil
}

// repoImports converts import paths to module-relative package directories,
// dropping everything outside this module's internal/ and cmd/.
func repoImports(modPath string, imps []string) []string {
	var out []string
	for _, imp := range imps {
		if rel, ok := strings.CutPrefix(imp, modPath+"/"); ok && (inPrefix(rel, "internal") || inPrefix(rel, "cmd")) {
			out = append(out, rel)
		}
	}
	return out
}
