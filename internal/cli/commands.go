package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shimagrawal/gitops-preflight/internal/config"
	"github.com/shimagrawal/gitops-preflight/internal/fluxschema"
	"github.com/shimagrawal/gitops-preflight/internal/render"
	"github.com/shimagrawal/gitops-preflight/internal/schemas"
	"github.com/shimagrawal/gitops-preflight/internal/steps"
)

func newRender(g *globals) *cobra.Command {
	var stage, out string
	cmd := &cobra.Command{
		Use:   "render [path]",
		Short: "Render a project's apps per stage, to look at what gets validated",
		Long: `Render each app listed under render: in the project's config.

  render ./my-app --stage prod    one stage, printed
  render ./my-app --out build     every stage into build/<stage>/<app>/manifests.yaml
  render ./my-app                 every stage, printed`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			c, err := config.Load(dir)
			if err != nil {
				return err
			}
			if len(c.Render) == 0 {
				return fmt.Errorf("%s lists nothing under render:", config.FileName)
			}
			var stages []string
			if stage != "" {
				stages = []string{stage}
			}
			outputs, err := render.Stages(cmd.Context(), c, stages...)
			if err != nil {
				return err
			}
			if out != "" {
				if err := render.WriteTree(out, outputs); err != nil {
					return err
				}
				for _, o := range outputs {
					fmt.Fprintln(g.out, filepath.Join(out, o.Path()))
				}
				return nil
			}
			for _, o := range outputs {
				fmt.Fprintf(g.out, "# ---- %s/%s\n%s\n", o.Stage, o.App, strings.TrimRight(string(o.Manifest), "\n"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&stage, "stage", "", "render only this stage")
	cmd.Flags().StringVar(&out, "out", "", "write <stage>/<app>/manifests.yaml under this folder")
	return cmd
}

func newSchemas(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "schemas [path...]",
		Short: "Download the schemas the projects are pinned to (for offline use)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs, err := projects(args)
			if err != nil {
				return err
			}
			for _, dir := range dirs {
				c, err := config.Load(dir)
				if err != nil {
					return err
				}
				set, err := schemas.Ensure(cmd.Context(), c.Versions)
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out, "%s: Kargo %s, Argo CD %s, Kubernetes %s\n", c.Name, c.Versions.Kargo, c.Versions.ArgoCD, c.Versions.Kubernetes)
				for _, d := range []string{set.Kargo, set.KargoSteps, set.ArgoCD, set.Kubernetes} {
					fmt.Fprintln(g.out, "  "+d)
				}
			}
			return nil
		},
	}
}

func newDoctor(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that gitops-preflight can run here",
		RunE: func(cmd *cobra.Command, args []string) error {
			ok := true
			fmt.Fprintf(g.out, "gitops-preflight %s\n", Version)
			if v, err := fluxschema.Version(cmd.Context()); err != nil {
				ok = false
				fmt.Fprintf(g.out, "  ✗ flux-schema: %v\n", err)
			} else {
				p, _ := exec.LookPath(fluxschema.Binary)
				fmt.Fprintf(g.out, "  ✓ flux-schema %s (%s)\n", v, p)
			}
			if dir, err := schemas.CacheDir(); err != nil {
				ok = false
				fmt.Fprintf(g.out, "  ✗ schema cache: %v\n", err)
			} else if err := os.MkdirAll(dir, 0o755); err != nil {
				ok = false
				fmt.Fprintf(g.out, "  ✗ schema cache %s: %v\n", dir, err)
			} else {
				fmt.Fprintf(g.out, "  ✓ schema cache %s\n", dir)
			}
			fmt.Fprintln(g.out, "  ✓ Helm rendering built in (no helm binary needed)")
			if !ok {
				return ExitError{Code: 1}
			}
			return nil
		},
	}
}

func newInit(g *globals) *cobra.Command {
	var kargo, argocd, kube string
	var force bool
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Create a " + config.FileName + " for a project",
		Long: `Create a ` + config.FileName + ` in a project folder. Stages are read from
the Kargo Stages found in its kargo/ folder; fill in render: yourself.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			path := filepath.Join(dir, config.FileName)
			if fileExists(path) && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			}
			name, _ := filepath.Abs(dir)
			stages := detectStages(filepath.Join(dir, "kargo"))
			stageList := "[dev, prod]"
			if len(stages) > 0 {
				stageList = "[" + strings.Join(stages, ", ") + "]"
			}
			content := fmt.Sprintf(initTemplate, filepath.Base(name), kargo, argocd, kube, stageList)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(g.out, "wrote %s — fill in render:, then run: gitops-preflight validate %s\n", path, dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&kargo, "kargo", "v1.12.2", "Kargo version to pin")
	cmd.Flags().StringVar(&argocd, "argocd", "v3.5.4", "Argo CD version to pin")
	cmd.Flags().StringVar(&kube, "kubernetes", "1.36.1", "Kubernetes version to pin")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}

// detectStages lists the Stage names in a folder's YAML, in file order.
func detectStages(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	sort.Strings(files)
	var stages []string
	for _, f := range files {
		docs, err := steps.ReadDocuments(f)
		if err != nil {
			continue
		}
		for _, d := range docs {
			if d["kind"] == "Stage" {
				if m, ok := d["metadata"].(map[string]any); ok {
					if n, ok := m["name"].(string); ok {
						stages = append(stages, n)
					}
				}
			}
		}
	}
	return stages
}

const initTemplate = `# gitops-preflight configuration: https://github.com/shimagrawal/gitops-preflight
name: %s

# Versions the checks are pinned to: match what you run.
versions:
  kargo: %s
  argocd: %s
  kubernetes: %s

# Where the resources live, relative to this folder (these are the defaults).
paths:
  kargo: kargo
  argocd: argocd

# Placeholders like ${GITOPS_REPO_URL} to fill in from the environment or .env.
# Kargo's ${{ ... }} expressions are never touched.
substitute: []

# Kinds without a public schema to skip, e.g. Akuity Platform kinds:
#   - ee.kargo.akuity.io/v1alpha1/CustomPromotionStep
skipKinds: []

# Steps whose config isn't checked, e.g. custom or Akuity Platform steps.
skipSteps: []

# Stages to render apps for, in promotion order.
stages: %s

# How each app is rendered per stage, mirroring your promotion's render steps.
# {stage} is replaced by the stage name. One of kustomize, helm or plain each.
render: []
#  - app: web
#    kustomize: apps/web/overlays/{stage}
#  - app: api
#    helm:
#      chart: apps/api
#      values: apps/api/values-{stage}.yaml
`
