// Package fluxschema runs the flux-schema CLI (github.com/fluxcd/flux-schema),
// the validation engine underneath gitops-preflight: JSON Schema and CEL
// validation, CRD schema extraction and Kustomize builds.
package fluxschema

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Binary is the flux-schema executable; override with $GITOPS_PREFLIGHT_FLUX_SCHEMA.
var Binary = "flux-schema"

// Result is one validated document from flux-schema's JSON report.
type Result struct {
	Resource *struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Namespace  string `json:"namespace,omitempty"`
		Name       string `json:"name,omitempty"`
	} `json:"resource"`
	Source     string      `json:"source"`
	Origin     string      `json:"origin,omitempty"`
	Index      int         `json:"idx"`
	Status     string      `json:"status"` // valid | invalid | skipped
	Reason     string      `json:"reason,omitempty"`
	Violations []Violation `json:"violations,omitempty"`
}

// Violation is one problem with a document.
type Violation struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// Summary counts results by status.
type Summary struct {
	Total   int `json:"total"`
	Valid   int `json:"valid"`
	Invalid int `json:"invalid"`
	Skipped int `json:"skipped"`
}

type report struct {
	Report struct {
		Summary Summary  `json:"summary"`
		Results []Result `json:"results"`
	} `json:"report"`
}

// ValidateOptions configures a validation run.
type ValidateOptions struct {
	// Schema locations, tried in order (first match wins).
	SchemaLocations []string
	// Kinds to skip ("Kind" or "group/version/Kind").
	SkipKinds []string
}

// Validate validates the given files or folders (folders with a kustomization
// are built with kustomize) and returns flux-schema's results. A run that
// finds invalid documents is not an error; failing to run is.
func Validate(ctx context.Context, paths []string, opts ValidateOptions) ([]Result, Summary, error) {
	args := []string{"validate", "-o", "json", "--concurrent", "1"}
	for _, s := range opts.SchemaLocations {
		args = append(args, "-s", s)
	}
	for _, k := range opts.SkipKinds {
		args = append(args, "--skip-kind", k)
	}
	args = append(args, paths...)
	out, stderr, err := run(ctx, args...)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, Summary{}, err
	}
	var r report
	if jerr := json.Unmarshal(out, &r); jerr != nil {
		if err != nil {
			return nil, Summary{}, fmt.Errorf("flux-schema validate failed: %s", strings.TrimSpace(stderr))
		}
		return nil, Summary{}, fmt.Errorf("reading flux-schema report: %w", jerr)
	}
	return r.Report.Results, r.Report.Summary, nil
}

// Build renders a folder with a kustomization (or plain YAML) into one YAML stream.
func Build(ctx context.Context, path string) ([]byte, error) {
	out, stderr, err := run(ctx, "build", path)
	if err != nil {
		return nil, fmt.Errorf("kustomize build of %s: %s", path, firstLine(stderr, err))
	}
	return out, nil
}

// ExtractCRDs writes JSON schemas for every CRD in files to dir.
func ExtractCRDs(ctx context.Context, dir string, files ...string) error {
	args := append([]string{"extract", "crd", "-d", dir, "--strip-description"}, files...)
	if _, stderr, err := run(ctx, args...); err != nil {
		return fmt.Errorf("extracting CRD schemas: %s", firstLine(stderr, err))
	}
	return nil
}

// ExtractKubernetes writes JSON schemas for a Kubernetes release to dir.
func ExtractKubernetes(ctx context.Context, dir, version string) error {
	if _, stderr, err := run(ctx, "extract", "k8s", "--version", strings.TrimPrefix(version, "v"), "-d", dir, "--strip-description"); err != nil {
		return fmt.Errorf("extracting Kubernetes %s schemas: %s", version, firstLine(stderr, err))
	}
	return nil
}

// Version returns flux-schema's version string, e.g. "0.15.0".
func Version(ctx context.Context) (string, error) {
	out, _, err := run(ctx, "--version")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", errors.New("empty version output")
	}
	return fields[len(fields)-1], nil
}

func run(ctx context.Context, args ...string) ([]byte, string, error) {
	cmd := exec.CommandContext(ctx, Binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, "", fmt.Errorf("%s is not installed: see https://github.com/fluxcd/flux-schema/releases", Binary)
	}
	return stdout.Bytes(), stderr.String(), err
}

func firstLine(stderr string, err error) string {
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return err.Error()
}
