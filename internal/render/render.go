// Package render builds each app's Kubernetes manifests per stage, the way a
// promotion's render steps would: Helm charts with the Helm SDK, Kustomize
// folders with flux-schema's kustomize build, plain YAML as is.
package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/getter"

	"github.com/shimagrawal/gitops-preflight/internal/config"
	"github.com/shimagrawal/gitops-preflight/internal/fluxschema"
)

// Output is one app rendered for one stage.
type Output struct {
	Stage    string
	App      string
	Manifest []byte
}

// Path is where Output lands in a rendered tree: <stage>/<app>/manifests.yaml,
// the layout of an artifact that holds every stage.
func (o Output) Path() string {
	return filepath.Join(o.Stage, o.App, "manifests.yaml")
}

// Stages renders every app for each of the given stages (all stages if none).
func Stages(ctx context.Context, c *config.Config, stages ...string) ([]Output, error) {
	if len(stages) == 0 {
		stages = c.Stages
	}
	var out []Output
	for _, stage := range stages {
		for _, app := range c.Render {
			m, err := App(ctx, c, app, stage)
			if err != nil {
				return out, fmt.Errorf("%s/%s: %w", stage, app.App, err)
			}
			out = append(out, Output{Stage: stage, App: app.App, Manifest: m})
		}
	}
	return out, nil
}

// App renders one app for one stage.
func App(ctx context.Context, c *config.Config, app config.RenderApp, stage string) ([]byte, error) {
	in := func(p string) string { return filepath.Join(c.Dir, config.WithStage(p, stage)) }
	switch {
	case app.Kustomize != "":
		return fluxschema.Build(ctx, in(app.Kustomize))
	case app.Helm != nil:
		var files []string
		for _, v := range app.Helm.Values {
			f := in(v)
			if _, err := os.Stat(f); err != nil {
				return nil, fmt.Errorf("values file %s not found", config.WithStage(v, stage))
			}
			files = append(files, f)
		}
		return helmTemplate(ctx, in(app.Helm.Chart), app.Helm.Release,
			config.WithStage(app.Helm.Namespace, stage), files, c.Versions.Kubernetes)
	case app.Plain != "":
		return plain(in(app.Plain))
	}
	return nil, fmt.Errorf("no kustomize, helm or plain source")
}

// WriteTree writes outputs under dir as <stage>/<app>/manifests.yaml.
func WriteTree(dir string, outputs []Output) error {
	for _, o := range outputs {
		p := filepath.Join(dir, o.Path())
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, o.Manifest, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// helmTemplate is `helm template`: a client-only dry-run install that never
// talks to a cluster. CRDs and hooks are included, as Kargo's helm-template
// step can include them.
func helmTemplate(ctx context.Context, chartPath, release, namespace string, valueFiles []string, kubeVersion string) ([]byte, error) {
	ch, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("loading chart: %w", err)
	}
	vals, err := (&values.Options{ValueFiles: valueFiles}).MergeValues(getter.Providers{})
	if err != nil {
		return nil, fmt.Errorf("reading values: %w", err)
	}
	inst := action.NewInstall(&action.Configuration{})
	inst.DryRun = true
	inst.ClientOnly = true
	inst.Replace = true
	inst.IncludeCRDs = true
	inst.ReleaseName = release
	inst.Namespace = namespace
	if inst.Namespace == "" {
		inst.Namespace = "default"
	}
	if kubeVersion != "" {
		kv, err := chartutil.ParseKubeVersion(kubeVersion)
		if err != nil {
			return nil, fmt.Errorf("kubernetes version %q: %w", kubeVersion, err)
		}
		inst.KubeVersion = kv
	}
	rel, err := inst.RunWithContext(ctx, ch, vals)
	if err != nil {
		return nil, fmt.Errorf("rendering chart: %w", err)
	}
	var b bytes.Buffer
	b.WriteString(rel.Manifest)
	for _, h := range rel.Hooks {
		fmt.Fprintf(&b, "\n---\n# Source: %s\n%s\n", h.Path, h.Manifest)
	}
	return b.Bytes(), nil
}

// plain concatenates the YAML files in a folder (or a single file).
func plain(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return os.ReadFile(path)
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")) {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var b bytes.Buffer
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "---\n# Source: %s\n", filepath.Base(f))
		b.Write(data)
		b.WriteString("\n")
	}
	return b.Bytes(), nil
}
