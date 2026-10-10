package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const versions = "versions: {kargo: v1.12.2, argocd: v3.5.4, kubernetes: 1.36.1}\n"

func TestDefaults(t *testing.T) {
	dir := write(t, versions+`
stages: [dev]
render:
  - app: web
    helm:
      chart: charts/web
      values: values-{stage}.yaml
`)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != filepath.Base(dir) {
		t.Errorf("Name = %q, want folder name", c.Name)
	}
	if c.Paths.Kargo != "kargo" || c.Paths.ArgoCD != "argocd" {
		t.Errorf("Paths = %+v, want defaults", c.Paths)
	}
	h := c.Render[0].Helm
	if h.Release != "web" {
		t.Errorf("Release = %q, want the app name", h.Release)
	}
	if len(h.Values) != 1 || h.Values[0] != "values-{stage}.yaml" {
		t.Errorf("Values = %v, want a single file accepted", h.Values)
	}
}

func TestValuesList(t *testing.T) {
	dir := write(t, versions+`
stages: [dev]
render:
  - app: web
    helm:
      chart: c
      values:
        - a.yaml
        - b-{stage}.yaml
`)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Render[0].Helm.Values; len(got) != 2 || got[1] != "b-{stage}.yaml" {
		t.Errorf("Values = %v", got)
	}
}

func TestErrors(t *testing.T) {
	for name, tc := range map[string]struct{ content, want string }{
		"missing versions":    {"name: x\n", "versions.kargo is required"},
		"unknown field":       {versions + "bogus: 1\n", `unknown field "bogus"`},
		"render needs stages": {versions + "render: [{app: a, kustomize: k}]\n", "stages is required"},
		"two sources":         {versions + "stages: [d]\nrender: [{app: a, kustomize: k, plain: p}]\n", "exactly one of"},
		"no source":           {versions + "stages: [d]\nrender: [{app: a}]\n", "exactly one of"},
		"duplicate app":       {versions + "stages: [d]\nrender: [{app: a, plain: p}, {app: a, plain: q}]\n", "listed twice"},
		"helm without chart":  {versions + "stages: [d]\nrender: [{app: a, helm: {values: v}}]\n", "helm.chart is required"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.content))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestNoConfig(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "has no "+FileName) {
		t.Fatalf("err = %v", err)
	}
}

func TestWithStage(t *testing.T) {
	if got := WithStage("apps/{stage}/x-{STAGE}", "prod"); got != "apps/prod/x-PROD" {
		t.Fatalf("got %q", got)
	}
}
