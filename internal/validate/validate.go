// Package validate runs a project's checks and turns the results into report
// sections:
//
//	kargo      Kargo resources vs Kargo's CRD schemas, CEL rules included (flux-schema)
//	steps      PromotionTask step configs vs Kargo's step schemas (package steps)
//	argocd     Argo CD resources vs Argo CD's CRD schemas (flux-schema)
//	manifests  each app rendered per stage vs Kubernetes schemas (package render + flux-schema)
package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shimagrawal/gitops-preflight/internal/config"
	"github.com/shimagrawal/gitops-preflight/internal/fluxschema"
	"github.com/shimagrawal/gitops-preflight/internal/render"
	"github.com/shimagrawal/gitops-preflight/internal/report"
	"github.com/shimagrawal/gitops-preflight/internal/schemas"
	"github.com/shimagrawal/gitops-preflight/internal/steps"
	"github.com/shimagrawal/gitops-preflight/internal/substitute"
)

// Check names.
const (
	Kargo     = "kargo"
	Steps     = "steps"
	ArgoCD    = "argocd"
	Manifests = "manifests"
)

// All checks, in report order.
var All = []string{Kargo, Steps, ArgoCD, Manifests}

// Options for a run.
type Options struct {
	// Checks to run; all when empty.
	Checks []string
	// Dotenv file with placeholder values; ignored when it doesn't exist.
	EnvFile string
}

// Result of validating one project.
type Result struct {
	Sections []report.Section
	// Placeholders listed in substitute that have no value.
	Unset []string
}

// Run validates one project.
func Run(ctx context.Context, c *config.Config, opts Options) (*Result, error) {
	checks := opts.Checks
	if len(checks) == 0 {
		checks = All
	}
	want := map[string]bool{}
	for _, ch := range checks {
		want[ch] = true
	}

	set, err := schemas.Ensure(ctx, c.Versions)
	if err != nil {
		return nil, err
	}

	// Extra CRD schemas (e.g. Akuity-only kinds) are tried before the pinned ones.
	var extra []string
	if len(c.Schemas.CRDs) > 0 {
		dir, err := schemas.ExtractExtraCRDs(ctx, c.Schemas.CRDs, c.Dir)
		if dir != "" {
			defer os.RemoveAll(filepath.Dir(dir))
		}
		if err != nil {
			return nil, err
		}
		extra = append(extra, dir)
	}

	sub, err := substitute.New(c.Substitute, opts.EnvFile)
	if err != nil {
		return nil, err
	}
	res := &Result{Unset: sub.Missing()}
	sort.Strings(res.Unset)

	work, err := os.MkdirTemp("", "gitops-preflight-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	// Placeholders filled in, written to a scratch copy; the project is untouched.
	kargoFiles, err := substituted(c, c.Paths.Kargo, filepath.Join(work, "kargo"), sub)
	if err != nil {
		return nil, err
	}
	argoFiles, err := substituted(c, c.Paths.ArgoCD, filepath.Join(work, "argocd"), sub)
	if err != nil {
		return nil, err
	}

	var sections []report.Section
	if want[Kargo] {
		s, err := resources(ctx, fmt.Sprintf("Kargo %s", c.Versions.Kargo), kargoFiles, c.Paths.Kargo,
			locations(extra, set.Kargo, set.Kubernetes), c.SkipKinds)
		if err != nil {
			return nil, err
		}
		sections = append(sections, s)
	}
	if want[Steps] {
		s, err := stepConfigs(c, set, kargoFiles)
		if err != nil {
			return nil, err
		}
		sections = append(sections, s)
	}
	if want[ArgoCD] {
		s, err := resources(ctx, fmt.Sprintf("Argo CD %s", c.Versions.ArgoCD), argoFiles, c.Paths.ArgoCD,
			locations(extra, set.ArgoCD, set.Kubernetes), c.SkipKinds)
		if err != nil {
			return nil, err
		}
		sections = append(sections, s)
	}
	if want[Manifests] {
		s, err := manifests(ctx, c, filepath.Join(work, "out"), locations(extra, set.Kubernetes))
		if err != nil {
			return nil, err
		}
		sections = append(sections, s)
	}
	res.Sections = sections
	return res, nil
}

// substituted copies <project>/<folder>/*.y*ml to dest with placeholders
// filled in, returning a map of scratch path → path relative to the project.
func substituted(c *config.Config, folder, dest string, sub *substitute.Substituter) (map[string]string, error) {
	files := map[string]string{}
	src := filepath.Join(c.Dir, folder)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return files, nil
	}
	var paths []string
	for _, pattern := range []string{"*.yaml", "*.yml"} {
		m, _ := filepath.Glob(filepath.Join(src, pattern))
		paths = append(paths, m...)
	}
	sort.Strings(paths)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out := filepath.Join(dest, filepath.Base(p))
		if err := os.WriteFile(out, []byte(sub.Apply(string(data))), 0o600); err != nil {
			return nil, err
		}
		files[out] = filepath.ToSlash(filepath.Join(folder, filepath.Base(p)))
	}
	return files, nil
}

// resources validates files with flux-schema.
func resources(ctx context.Context, title string, files map[string]string, folder string, locations, skip []string) (report.Section, error) {
	s := report.Section{Title: title}
	if len(files) == 0 {
		s.Lines = append(s.Lines, report.Line{Status: report.Skip, Text: fmt.Sprintf("no YAML files in %s/", folder)})
		return s, nil
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	results, sum, err := fluxschema.Validate(ctx, paths, fluxschema.ValidateOptions{SchemaLocations: locations, SkipKinds: skip})
	if err != nil {
		return s, err
	}
	for _, r := range results {
		s.Lines = append(s.Lines, line(r, files[r.Source]))
	}
	s.Summary = summary(sum, len(files), "file")
	return s, nil
}

// stepConfigs lints every PromotionTask / ClusterPromotionTask.
func stepConfigs(c *config.Config, set *schemas.Set, kargoFiles map[string]string) (report.Section, error) {
	s := report.Section{Title: fmt.Sprintf("Kargo step configs %s", c.Versions.Kargo)}
	dirs := append([]string{}, c.Schemas.Steps...)
	for i, d := range dirs {
		if !filepath.IsAbs(d) {
			dirs[i] = filepath.Join(c.Dir, d)
		}
	}
	dirs = append(dirs, set.KargoSteps) // project-supplied schemas win
	skip := map[string]bool{}
	for _, st := range c.SkipSteps {
		skip[st] = true
	}
	linter := &steps.Linter{Schemas: steps.NewSchemas(dirs...), Skip: skip}

	paths := make([]string, 0, len(kargoFiles))
	for p := range kargoFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		tasks, err := steps.ReadTasks(p)
		if err != nil {
			s.Lines = append(s.Lines, report.Line{Status: report.Fail, Text: "cannot read file", Source: kargoFiles[p], Details: []string{err.Error()}})
			continue
		}
		for _, t := range tasks {
			kind, _ := t["kind"].(string)
			meta, _ := t["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			var errs, warns []string
			for _, f := range linter.Lint(t) {
				if f.Warning {
					warns = append(warns, f.Message)
				} else {
					errs = append(errs, f.Message)
				}
			}
			n := 0
			if spec, ok := t["spec"].(map[string]any); ok {
				if st, ok := spec["steps"].([]any); ok {
					n = len(st)
				}
			}
			label := fmt.Sprintf("%s/%s", kind, name)
			switch {
			case len(errs) > 0:
				text := label + ": " + report.Plural(len(errs), "problem")
				if len(warns) > 0 {
					text += ", " + report.Plural(len(warns), "warning")
				}
				s.Lines = append(s.Lines, report.Line{Status: report.Fail, Text: text, Source: kargoFiles[p], Details: append(errs, warns...)})
			case len(warns) > 0:
				s.Lines = append(s.Lines, report.Line{Status: report.Warn, Text: fmt.Sprintf("%s: %s, %s", label, report.Plural(n, "step"), report.Plural(len(warns), "warning")), Source: kargoFiles[p], Details: warns})
			default:
				s.Lines = append(s.Lines, report.Line{Status: report.OK, Text: fmt.Sprintf("%s: %s", label, report.Plural(n, "step")), Source: kargoFiles[p]})
			}
		}
	}
	return s, nil
}

// manifests renders every app for every stage, then validates the output.
func manifests(ctx context.Context, c *config.Config, outDir string, locations []string) (report.Section, error) {
	s := report.Section{Title: fmt.Sprintf("Rendered manifests (Kubernetes %s)", c.Versions.Kubernetes)}
	if len(c.Render) == 0 {
		s.Lines = append(s.Lines, report.Line{Status: report.Skip, Text: "nothing to render (no render: in " + config.FileName + ")"})
		return s, nil
	}
	files := map[string]string{}
	var outputs []render.Output
	for _, stage := range c.Stages {
		for _, app := range c.Render {
			m, err := render.App(ctx, c, app, stage)
			if err != nil {
				s.Lines = append(s.Lines, report.Line{Status: report.Fail, Text: "render failed", Source: stage + "/" + app.App, Details: []string{err.Error()}})
				continue
			}
			o := render.Output{Stage: stage, App: app.App, Manifest: m}
			outputs = append(outputs, o)
			files[filepath.Join(outDir, o.Path())] = stage + "/" + app.App
		}
	}
	if err := render.WriteTree(outDir, outputs); err != nil {
		return s, err
	}
	if len(files) == 0 {
		return s, nil
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	results, sum, err := fluxschema.Validate(ctx, paths, fluxschema.ValidateOptions{SchemaLocations: locations, SkipKinds: c.SkipKinds})
	if err != nil {
		return s, err
	}
	for _, r := range results {
		s.Lines = append(s.Lines, line(r, files[r.Source]))
	}
	s.Summary = summary(sum, len(files), "file")
	return s, nil
}

// locations returns extra schema locations followed by more, as a new slice.
func locations(extra []string, more ...string) []string {
	return append(append([]string{}, extra...), more...)
}

func line(r fluxschema.Result, source string) report.Line {
	name := "document " + fmt.Sprint(r.Index)
	if r.Resource != nil && r.Resource.Kind != "" {
		parts := []string{r.Resource.Kind}
		if r.Resource.Namespace != "" {
			parts = append(parts, r.Resource.Namespace)
		}
		if r.Resource.Name != "" {
			parts = append(parts, r.Resource.Name)
		}
		name = strings.Join(parts, "/")
	}
	l := report.Line{Text: name, Source: source}
	switch r.Status {
	case "valid":
		l.Status = report.OK
	case "skipped":
		l.Status = report.Skip
		l.Text += ": skipped"
	default:
		l.Status = report.Fail
		l.Text += ": " + report.Reason(r.Reason)
	}
	for _, v := range r.Violations {
		if v.Path != "" {
			l.Details = append(l.Details, v.Path+": "+v.Message)
		} else {
			l.Details = append(l.Details, v.Message)
		}
	}
	return l
}

func summary(s fluxschema.Summary, files int, unit string) string {
	return fmt.Sprintf("%d resources found in %s - Valid: %d, Invalid: %d, Skipped: %d",
		s.Total, report.Plural(files, unit), s.Valid, s.Invalid, s.Skipped)
}
