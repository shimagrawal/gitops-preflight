package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shimagrawal/gitops-preflight/internal/config"
	"github.com/shimagrawal/gitops-preflight/internal/report"
	"github.com/shimagrawal/gitops-preflight/internal/validate"
)

func newValidate(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [path...]",
		Short: "Run every check (kargo, steps, argocd, manifests)",
		Long: `Run every check on each project. A path is a project folder or a folder to
search for projects; the default is the current folder.

Every check runs even after a failure, so all problems show at once.
Exit code: 0 all valid, 1 something is invalid, 2 could not run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(cmd, g, args, nil)
		},
	}
	for _, check := range []struct{ name, short string }{
		{validate.Kargo, "Kargo resources vs Kargo's CRD schemas (CEL rules included)"},
		{validate.Steps, "Step configs inside PromotionTasks vs Kargo's step schemas"},
		{validate.ArgoCD, "Argo CD resources vs Argo CD's CRD schemas"},
		{validate.Manifests, "Render every app per stage and validate against Kubernetes"},
	} {
		check := check
		cmd.AddCommand(&cobra.Command{
			Use:   check.name + " [path...]",
			Short: check.short,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runValidate(cmd, g, args, []string{check.name})
			},
		})
	}
	return cmd
}

func runValidate(cmd *cobra.Command, g *globals, args, checks []string) error {
	dirs, err := projects(args)
	if err != nil {
		return err
	}
	ok := true
	for _, dir := range dirs {
		c, err := config.Load(dir)
		if err != nil {
			return err
		}
		if len(dirs) > 1 {
			fmt.Fprintf(g.out, "\n### %s (%s)\n", c.Name, rel(c.Dir))
		}
		res, err := validate.Run(cmd.Context(), c, validate.Options{Checks: checks, EnvFile: g.envFileFor(c)})
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
		if len(res.Unset) > 0 {
			fmt.Fprintf(g.errOut, "note: no value for %s (set it in the environment or %s)\n",
				strings.Join(res.Unset, ", "), g.envFileFor(c))
		}
		report.Print(g.out, res.Sections)
		if !report.Verdict(g.out, res.Sections) {
			ok = false
		}
	}
	if !ok {
		return ExitError{Code: 1}
	}
	return nil
}
