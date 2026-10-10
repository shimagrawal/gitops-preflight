// Package schemas downloads and caches the schemas a project is pinned to:
// Kargo CRDs and step config schemas, Argo CD CRDs and Kubernetes.
//
// Cache layout ($GITOPS_PREFLIGHT_CACHE, else $XDG_CACHE_HOME/gitops-preflight,
// else ~/.cache/gitops-preflight):
//
//	kargo-<version>/        JSON schemas extracted from Kargo's CRDs
//	kargo-steps-<version>/  Kargo's built-in step config schemas, <step>.json
//	argocd-<version>/       JSON schemas extracted from Argo CD's CRDs
//	k8s-<version>/          JSON schemas for a Kubernetes release
//
// Sources at a pinned version never change, so a cached folder is never
// refreshed; delete it to force a download.
package schemas

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/shimagrawal/gitops-preflight/internal/config"
	"github.com/shimagrawal/gitops-preflight/internal/fluxschema"
)

// Set is the schema folders for one set of versions.
type Set struct {
	Kargo      string
	KargoSteps string
	ArgoCD     string
	Kubernetes string
}

// Logf, when set, receives progress messages (downloads only).
var Logf = func(string, ...any) {}

// CacheDir returns the cache root.
func CacheDir() (string, error) {
	if d := os.Getenv("GITOPS_PREFLIGHT_CACHE"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "gitops-preflight"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "gitops-preflight"), nil
}

// Ensure makes sure the schemas for v are cached and returns their folders.
func Ensure(ctx context.Context, v config.Versions) (*Set, error) {
	root, err := CacheDir()
	if err != nil {
		return nil, err
	}
	s := &Set{
		Kargo:      filepath.Join(root, "kargo-"+v.Kargo),
		KargoSteps: filepath.Join(root, "kargo-steps-"+v.Kargo),
		ArgoCD:     filepath.Join(root, "argocd-"+v.ArgoCD),
		Kubernetes: filepath.Join(root, "k8s-"+strings.TrimPrefix(v.Kubernetes, "v")),
	}
	if !exists(s.Kargo) || !exists(s.KargoSteps) {
		if err := fetchKargo(ctx, v.Kargo, s.Kargo, s.KargoSteps); err != nil {
			return nil, err
		}
	}
	if !exists(s.ArgoCD) {
		if err := fetchArgoCD(ctx, v.ArgoCD, s.ArgoCD); err != nil {
			return nil, err
		}
	}
	if !exists(s.Kubernetes) {
		Logf("Kubernetes %s schemas", v.Kubernetes)
		if err := intoDir(s.Kubernetes, func(tmp string) error {
			return fluxschema.ExtractKubernetes(ctx, tmp, v.Kubernetes)
		}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ExtractExtraCRDs extracts schemas from CRD files or URLs into a fresh
// temporary folder, which the caller removes.
func ExtractExtraCRDs(ctx context.Context, sources []string, baseDir string) (string, error) {
	dir, err := os.MkdirTemp("", "gitops-preflight-crds-")
	if err != nil {
		return "", err
	}
	var files []string
	for i, src := range sources {
		if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
			f := filepath.Join(dir, fmt.Sprintf("download-%d.yaml", i))
			if err := download(ctx, src, f); err != nil {
				return dir, err
			}
			files = append(files, f)
			continue
		}
		if !filepath.IsAbs(src) {
			src = filepath.Join(baseDir, src)
		}
		files = append(files, src)
	}
	out := filepath.Join(dir, "schemas")
	if err := fluxschema.ExtractCRDs(ctx, out, files...); err != nil {
		return dir, err
	}
	return out, nil
}

// fetchKargo downloads a Kargo release once and extracts both its CRD schemas
// and its built-in step config schemas.
func fetchKargo(ctx context.Context, version, crdDir, stepDir string) error {
	Logf("Kargo %s schemas", version)
	work, err := os.MkdirTemp("", "gitops-preflight-kargo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	url := fmt.Sprintf("https://github.com/akuity/kargo/archive/refs/tags/%s.tar.gz", version)
	crds, steps := filepath.Join(work, "crds"), filepath.Join(work, "steps")
	if err := untarMatching(ctx, url, map[string]string{
		"charts/kargo/resources/crds/":          crds,
		"pkg/promotion/runner/builtin/schemas/": steps,
	}); err != nil {
		return fmt.Errorf("downloading Kargo %s: %w", version, err)
	}

	crdFiles, _ := filepath.Glob(filepath.Join(crds, "*.yaml"))
	if len(crdFiles) == 0 {
		return fmt.Errorf("no CRDs found in Kargo %s", version)
	}
	if err := intoDir(crdDir, func(tmp string) error {
		return fluxschema.ExtractCRDs(ctx, tmp, crdFiles...)
	}); err != nil {
		return err
	}

	// Step schemas are found by step name: git-clone-config.json -> git-clone.json.
	// Shared files such as argocd-common.json keep their name.
	return intoDir(stepDir, func(tmp string) error {
		files, _ := filepath.Glob(filepath.Join(steps, "*.json"))
		if len(files) == 0 {
			return fmt.Errorf("no step schemas found in Kargo %s", version)
		}
		for _, f := range files {
			name := strings.TrimSuffix(filepath.Base(f), ".json")
			name = strings.TrimSuffix(name, "-config") + ".json"
			if err := copyFile(f, filepath.Join(tmp, name)); err != nil {
				return err
			}
		}
		return nil
	})
}

func fetchArgoCD(ctx context.Context, version, dir string) error {
	Logf("Argo CD %s schemas", version)
	work, err := os.MkdirTemp("", "gitops-preflight-argocd-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	var files []string
	for _, crd := range []string{"application", "applicationset", "appproject"} {
		f := filepath.Join(work, crd+"-crd.yaml")
		url := fmt.Sprintf("https://raw.githubusercontent.com/argoproj/argo-cd/%s/manifests/crds/%s-crd.yaml", version, crd)
		if err := download(ctx, url, f); err != nil {
			return fmt.Errorf("downloading Argo CD %s: %w", version, err)
		}
		files = append(files, f)
	}
	return intoDir(dir, func(tmp string) error {
		return fluxschema.ExtractCRDs(ctx, tmp, files...)
	})
}

// intoDir fills dir via fill(tmp) and only then moves it into place, so an
// interrupted download never leaves a half-filled cache folder behind.
func intoDir(dir string, fill func(tmp string) error) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "."+filepath.Base(dir)+"-")
	if err != nil {
		return err
	}
	if err := fill(tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

// untarMatching streams a .tar.gz and writes entries whose path (after the
// archive's top folder) starts with one of the prefixes into the mapped folder.
func untarMatching(ctx context.Context, url string, prefixes map[string]string) error {
	body, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	gz, err := gzip.NewReader(body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		_, rel, ok := strings.Cut(h.Name, "/") // drop "kargo-1.12.2/"
		if !ok {
			continue
		}
		for prefix, dest := range prefixes {
			if !strings.HasPrefix(rel, prefix) || strings.Contains(strings.TrimPrefix(rel, prefix), "/") {
				continue
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			f, err := os.Create(filepath.Join(dest, path.Base(rel)))
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}

func download(ctx context.Context, url, file string) error {
	body, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, body)
	return err
}

func get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func exists(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}
