// Package project finds projects: folders that hold a gitops-preflight.yaml.
package project

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shimagrawal/gitops-preflight/internal/config"
)

// Discover returns every folder under root (root included) that holds a
// gitops-preflight.yaml, sorted. Hidden folders, vendor and node_modules are
// skipped.
func Discover(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, config.FileName)); err == nil {
				dirs = append(dirs, path)
			}
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}
