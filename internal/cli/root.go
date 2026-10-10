// Package cli is the gitops-preflight command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/shimagrawal/gitops-preflight/internal/config"
	"github.com/shimagrawal/gitops-preflight/internal/project"
	"github.com/shimagrawal/gitops-preflight/internal/schemas"
)

// Version is set at build time (-ldflags "-X .../internal/cli.Version=v0.1.0").
var Version = "dev"

// ExitError carries an exit code without printing anything more.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

type globals struct {
	envFile string
	out     io.Writer
	errOut  io.Writer
}

// New returns the root command.
func New() *cobra.Command {
	g := &globals{out: os.Stdout, errOut: os.Stderr}
	root := &cobra.Command{
		Use:   "gitops-preflight",
		Short: "Validate Kargo, Argo CD and Kubernetes manifests before they are promoted",
		Long: `gitops-preflight checks a GitOps project locally, against schemas pinned to
the Kargo, Argo CD and Kubernetes versions you run:

  kargo      Kargo resources vs Kargo's CRDs, CEL rules included
  steps      step configs inside PromotionTasks vs Kargo's step schemas
  argocd     Argo CD resources vs Argo CD's CRDs
  manifests  every app rendered per stage (Helm, Kustomize, plain YAML) vs Kubernetes

A project is a folder with a ` + config.FileName + ` (see "gitops-preflight init").
Validation runs on flux-schema (https://github.com/fluxcd/flux-schema).`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	root.PersistentFlags().StringVar(&g.envFile, "env-file", "",
		"dotenv file with placeholder values (default: the project's .env, else ./.env)")
	schemas.Logf = func(format string, a ...any) {
		fmt.Fprintf(g.errOut, "downloading "+format+"\n", a...)
	}
	root.AddCommand(
		newValidate(g),
		newRender(g),
		newSchemas(g),
		newInit(g),
		newDoctor(g),
	)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := New().Execute(); err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee.Code
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return 0
}

// projects resolves arguments to project folders: a folder with a config file
// is a project; any other folder is searched for projects. No arguments means
// the current folder.
func projects(args []string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"."}
	}
	var dirs []string
	for _, a := range args {
		if _, err := os.Stat(filepath.Join(a, config.FileName)); err == nil {
			dirs = append(dirs, a)
			continue
		}
		if st, err := os.Stat(a); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("%s is not a folder", a)
		}
		found, err := project.Discover(a)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, found...)
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no %s found (create one with \"gitops-preflight init\")", config.FileName)
	}
	return dirs, nil
}

// envFile picks the dotenv file for a project.
func (g *globals) envFileFor(c *config.Config) string {
	if g.envFile != "" {
		return g.envFile
	}
	if p := filepath.Join(c.Dir, ".env"); fileExists(p) {
		return p
	}
	return ".env"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func rel(dir string) string {
	wd, err := os.Getwd()
	if err != nil {
		return dir
	}
	if r, err := filepath.Rel(wd, dir); err == nil {
		return r
	}
	return dir
}
