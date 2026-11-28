package policy

import "strings"

// WithToolchain returns in with the session toolchain (D-085, real paths):
// pathDirs first on Path, readDirs added to ReadPaths. The slices of in are
// not modified.
func (in Inputs) WithToolchain(pathDirs, readDirs []string) Inputs {
	if len(pathDirs) > 0 {
		path := strings.Join(pathDirs, ":")
		if in.Path != "" {
			path += ":" + in.Path
		}
		in.Path = path
	}
	in.ReadPaths = append(append([]string{}, in.ReadPaths...), readDirs...)
	return in
}
