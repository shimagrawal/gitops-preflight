// Command gitops-preflight validates Kargo, Argo CD and Kubernetes manifests
// against schemas pinned to the versions you run.
package main

import (
	"os"

	"github.com/shimagrawal/gitops-preflight/internal/cli"
	"github.com/shimagrawal/gitops-preflight/internal/fluxschema"
)

func main() {
	if b := os.Getenv("GITOPS_PREFLIGHT_FLUX_SCHEMA"); b != "" {
		fluxschema.Binary = b
	}
	os.Exit(cli.Execute())
}
