package validate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shimagrawal/gitops-preflight/internal/config"
	"github.com/shimagrawal/gitops-preflight/internal/fluxschema"
	"github.com/shimagrawal/gitops-preflight/internal/report"
)

// These tests run the real checks on examples/oci-flow: they need flux-schema
// on PATH and download schemas on first use (cached in $GITOPS_PREFLIGHT_CACHE).
func requireFluxSchema(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(fluxschema.Binary); err != nil {
		t.Skip("flux-schema not installed")
	}
	if testing.Short() {
		t.Skip("integration test")
	}
}

// copyExample copies examples/oci-flow into a temp folder.
func copyExample(t *testing.T) string {
	t.Helper()
	src, _ := filepath.Abs("../../examples/oci-flow")
	dst := filepath.Join(t.TempDir(), "oci-flow")
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func run(t *testing.T, dir string) *Result {
	t.Helper()
	c, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), c, Options{EnvFile: filepath.Join(dir, ".env")})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func failures(res *Result) map[string][]string {
	out := map[string][]string{}
	for _, s := range res.Sections {
		for _, l := range s.Lines {
			if l.Status == report.Fail {
				out[s.Title] = append(out[s.Title], l.Text+" "+strings.Join(l.Details, " | "))
			}
		}
	}
	return out
}

func TestExampleIsValid(t *testing.T) {
	requireFluxSchema(t)
	res := run(t, copyExample(t))
	if f := failures(res); len(f) != 0 {
		t.Fatalf("example has failures: %v", f)
	}
	if len(res.Sections) != 4 {
		t.Fatalf("want 4 sections, got %d", len(res.Sections))
	}
	if len(res.Unset) != 0 {
		t.Fatalf("unset placeholders: %v", res.Unset)
	}
}

func TestEachCheckCatchesItsMistake(t *testing.T) {
	requireFluxSchema(t)
	for name, tc := range map[string]struct{ file, from, to, section, want string }{
		"kargo":     {"kargo/stages.yaml", "requestedFreight:", "requestedFrieght:", "Kargo v", "requestedFrieght"},
		"steps":     {"kargo/promotiontasks.yaml", "inPath: ./out", "inPath: ${{ vars.outDir }}", "Kargo step configs", "vars.outDir is referenced but not declared"},
		"argocd":    {"argocd/applicationset.yaml", "      syncPolicy:", "      syncPolcy:", "Argo CD", "syncPolcy"},
		"manifests": {"apps/hello/templates/deployment.yaml", "  replicas: {{", "  replica: {{", "Rendered manifests", "'replica' not allowed"},
		"render":    {"gitops-preflight.yaml", "values-{stage}.yaml", "values-missing-{stage}.yaml", "Rendered manifests", "values file apps/hello/values-missing-dev.yaml not found"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := copyExample(t)
			p := filepath.Join(dir, tc.file)
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tc.from) {
				t.Fatalf("test setup: %q not in %s", tc.from, tc.file)
			}
			if err := os.WriteFile(p, []byte(strings.Replace(string(data), tc.from, tc.to, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			f := failures(run(t, dir))
			if len(f) != 1 {
				t.Fatalf("want failures in exactly one section, got %v", f)
			}
			for title, lines := range f {
				if !strings.HasPrefix(title, tc.section) || !strings.Contains(strings.Join(lines, "\n"), tc.want) {
					t.Fatalf("want %q in section %q, got %s: %v", tc.want, tc.section, title, lines)
				}
			}
		})
	}
}

func TestSkipKinds(t *testing.T) {
	requireFluxSchema(t)
	dir := copyExample(t)
	cm := "apiVersion: ee.kargo.akuity.io/v1alpha1\nkind: MessageChannel\nmetadata: {name: slack, namespace: oci-flow}\nspec: {}\n"
	if err := os.WriteFile(filepath.Join(dir, "kargo", "channel.yaml"), []byte(cm), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := failures(run(t, dir)); len(f["Kargo v1.12.2"]) != 1 {
		t.Fatalf("without skipKinds want 1 kargo failure (schema not found), got %v", f)
	}
	cfg := filepath.Join(dir, config.FileName)
	data, _ := os.ReadFile(cfg)
	data = append(data, []byte("skipKinds: [ee.kargo.akuity.io/v1alpha1/MessageChannel]\n")...)
	if err := os.WriteFile(cfg, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if f := failures(run(t, dir)); len(f) != 0 {
		t.Fatalf("with skipKinds want no failures, got %v", f)
	}
}
