// Package config loads a project's gitops-preflight.yaml.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// FileName is the per-project config file. A folder that holds one is a project.
const FileName = "gitops-preflight.yaml"

// Config is the content of gitops-preflight.yaml.
type Config struct {
	// Name of the project, used in output. Defaults to the folder name.
	Name string `json:"name,omitempty"`
	// Versions the checks are pinned to.
	Versions Versions `json:"versions"`
	// Paths of the resource folders, relative to the project.
	Paths Paths `json:"paths,omitempty"`
	// Placeholders (${NAME}) to fill in from the environment or a .env file.
	// Anything else, including Kargo's ${{ ... }}, is left untouched.
	Substitute []string `json:"substitute,omitempty"`
	// Kinds to skip, as "Kind" or "group/version/Kind", e.g. Akuity-only kinds
	// that have no public schema.
	SkipKinds []string `json:"skipKinds,omitempty"`
	// Promotion steps whose config is not checked, e.g. custom steps.
	SkipSteps []string `json:"skipSteps,omitempty"`
	// Extra schemas on top of the pinned open-source ones.
	Schemas ExtraSchemas `json:"schemas,omitempty"`
	// Stages to render for, in order.
	Stages []string `json:"stages,omitempty"`
	// How to render each app per stage. {stage} is replaced by the stage name.
	Render []RenderApp `json:"render,omitempty"`

	// Dir is the project folder (absolute). Not read from the file.
	Dir string `json:"-"`
}

// Versions of Kargo, Argo CD and Kubernetes the schemas are pinned to.
type Versions struct {
	Kargo      string `json:"kargo,omitempty"`
	ArgoCD     string `json:"argocd,omitempty"`
	Kubernetes string `json:"kubernetes,omitempty"`
}

// Paths of a project's resource folders.
type Paths struct {
	Kargo  string `json:"kargo,omitempty"`
	ArgoCD string `json:"argocd,omitempty"`
}

// ExtraSchemas adds schemas the open-source catalogs lack, e.g. Akuity-only CRDs.
type ExtraSchemas struct {
	// CRD files (or URLs) to extract schemas from.
	CRDs []string `json:"crds,omitempty"`
	// Folders with step config schemas, one <step>.json per step.
	Steps []string `json:"steps,omitempty"`
}

// RenderApp says how one app is rendered. Exactly one of Kustomize, Helm or
// Plain is set.
type RenderApp struct {
	App       string   `json:"app"`
	Kustomize string   `json:"kustomize,omitempty"`
	Helm      *HelmApp `json:"helm,omitempty"`
	Plain     string   `json:"plain,omitempty"`
}

// HelmApp renders a local chart.
type HelmApp struct {
	Chart     string `json:"chart"`
	Release   string `json:"release,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// Values files, merged in order. One file or a list.
	Values StringList `json:"values,omitempty"`
}

// StringList accepts either a single string or a list of strings.
type StringList []string

// UnmarshalJSON implements json.Unmarshaler.
func (l *StringList) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*l = StringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("expected a string or a list of strings")
	}
	*l = many
	return nil
}

// Load reads and checks dir/gitops-preflight.yaml.
func Load(dir string) (*Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(abs, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s has no %s", dir, FileName)
		}
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Dir = abs
	c.applyDefaults()
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Name == "" {
		c.Name = filepath.Base(c.Dir)
	}
	if c.Paths.Kargo == "" {
		c.Paths.Kargo = "kargo"
	}
	if c.Paths.ArgoCD == "" {
		c.Paths.ArgoCD = "argocd"
	}
	for i := range c.Render {
		if h := c.Render[i].Helm; h != nil && h.Release == "" {
			h.Release = c.Render[i].App
		}
	}
}

func (c *Config) check() error {
	var problems []string
	if c.Versions.Kargo == "" {
		problems = append(problems, "versions.kargo is required (e.g. v1.12.2)")
	}
	if c.Versions.ArgoCD == "" {
		problems = append(problems, "versions.argocd is required (e.g. v3.5.4)")
	}
	if c.Versions.Kubernetes == "" {
		problems = append(problems, "versions.kubernetes is required (e.g. 1.36.1)")
	}
	if len(c.Render) > 0 && len(c.Stages) == 0 {
		problems = append(problems, "stages is required when render is set")
	}
	seen := map[string]bool{}
	for i, r := range c.Render {
		at := fmt.Sprintf("render[%d]", i)
		if r.App == "" {
			problems = append(problems, at+".app is required")
		} else if seen[r.App] {
			problems = append(problems, fmt.Sprintf("%s: app %q is listed twice", at, r.App))
		}
		seen[r.App] = true
		set := 0
		if r.Kustomize != "" {
			set++
		}
		if r.Helm != nil {
			set++
			if r.Helm.Chart == "" {
				problems = append(problems, at+".helm.chart is required")
			}
		}
		if r.Plain != "" {
			set++
		}
		if set != 1 {
			problems = append(problems, at+" needs exactly one of kustomize, helm or plain")
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// WithStage replaces {stage} and {STAGE} (upper case) in s.
func WithStage(s, stage string) string {
	s = strings.ReplaceAll(s, "{stage}", stage)
	return strings.ReplaceAll(s, "{STAGE}", strings.ToUpper(stage))
}
