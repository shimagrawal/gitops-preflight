# Example: oci-flow

Kargo renders two apps for every stage, nginx (Kustomize) and hello (Helm),
packs them into one OCI artifact, and every stage's Argo CD Application deploys
that same digest.

```bash
gitops-preflight validate examples/oci-flow
gitops-preflight render examples/oci-flow --stage prod
```

`.env` holds example values for the placeholders listed under `substitute:`.
