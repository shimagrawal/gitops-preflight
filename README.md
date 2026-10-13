# gitops-preflight

Pre-flight checks for GitOps delivery: validate your **Kargo**, **Argo CD** and
rendered **Kubernetes** manifests locally or in CI, against schemas pinned to
the versions you actually run, before anything is promoted.

> **The problem:** mistakes in Kargo and Argo CD YAML often only show up during
> a promotion, sometimes in prod.
> [flux-schema](https://github.com/fluxcd/flux-schema) is a great validator,
> but it only checks what CRD schemas describe.
> gitops-preflight builds on it and catches what flux-schema can't, in one
> command, locally or in CI.

On top of schema validation, it adds:

- **Step configs:** what's inside each step's `config:` is checked against
  Kargo's step schemas. Kargo's CRD accepts any config, so CRD-based
  validation can't see a typo there.
- **Pinned versions:** it downloads and caches the Kargo, Argo CD and
  Kubernetes schemas for the exact versions in `gitops-preflight.yaml`. With
  flux-schema alone you fetch and extract them yourself, per version.
- **Rendering:** it renders Helm (built in) and Kustomize per stage, so the
  rendered manifests are validated, not just the sources.
- **Akuity-only kinds and steps** (`ee.kargo.akuity.io`, `send-message`,
  custom steps): no public schemas exist, so you can bring your own CRDs and
  step schemas or skip them explicitly (see [Akuity Platform](#akuity-platform)).

```console
$ gitops-preflight validate ./oci-flow
Kargo v1.12.2
  ✓ Stage/oci-flow/dev   (kargo/stages.yaml)
  ✗ Stage/oci-flow/test: schema violation   (kargo/stages.yaml)
      - /spec: additional properties 'requestedFrieght' not allowed
Kargo step configs v1.12.2
  ✗ PromotionTask/oci-deploy: 1 problem   (kargo/promotiontasks.yaml)
      - spec.steps[0] (argocd-update): config.apps[0].sources[0]: unknown key "desiredRevison" (...)
Argo CD v3.5.4
  ✓ ApplicationSet/argocd/oci-flow   (argocd/applicationset.yaml)
Rendered manifests (Kubernetes 1.36.1)
  ✗ Deployment/hello: schema violation   (prod/hello)
      - /spec: additional properties 'replica' not allowed

✗ Validation failed (see ✗ above)
```

## What it checks

| Check | What | Against |
|---|---|---|
| `kargo` | Kargo resources: Project, Warehouse, Stage, PromotionTask, ... | Kargo's CRD schemas, **CEL rules included** |
| `steps` | What's *inside* each step's `config:` in PromotionTasks, plus `vars` / `task.outputs` wiring | Kargo's built-in step schemas (Kargo's CRD allows any config, so nothing else catches these before a promotion runs) |
| `argocd` | Application, ApplicationSet, AppProject | Argo CD's CRD schemas |
| `manifests` | Every app rendered per stage: Helm (built in), Kustomize, plain YAML | Kubernetes, at your cluster's version |

Every check runs even after a failure, so you see all problems at once. Exit
code `0` all valid, `1` something is invalid, `2` could not run.

Schemas are downloaded once per version and cached in
`~/.cache/gitops-preflight`. Validation runs on
[flux-schema](https://github.com/fluxcd/flux-schema), which evaluates CRD CEL
rules with the same engine as the Kubernetes API server.

## Install

| How | Command |
|---|---|
| **Docker** (nothing else needed) | `docker run --rm -v "$PWD:/work" ghcr.io/shimagrawal/gitops-preflight validate` |
| **GitHub Action** | see [CI](#ci) |
| **Homebrew** | coming later |
| **Binary** | download from [Releases](https://github.com/shimagrawal/gitops-preflight/releases) |
| **Go** | `go install github.com/shimagrawal/gitops-preflight/cmd/gitops-preflight@latest` |

Outside Docker you also need [flux-schema](https://github.com/fluxcd/flux-schema/releases)
on your `PATH`. `gitops-preflight doctor` checks your setup. Helm rendering is
built in; no `helm` or `kubectl` binary is needed.

## Quickstart

```bash
cd my-gitops-repo
gitops-preflight init my-app        # writes my-app/gitops-preflight.yaml (stages read from kargo/)
$EDITOR my-app/gitops-preflight.yaml  # pin your versions, add render: entries
gitops-preflight validate           # every project in the repo
```

A **project** is any folder with a `gitops-preflight.yaml`:

```yaml
name: my-app
versions:              # match what you run
  kargo: v1.12.2
  argocd: v3.5.4
  kubernetes: 1.36.1
substitute:            # ${NAME} placeholders filled from the environment or .env
  - GITOPS_REPO_URL
stages: [dev, test, prod]
render:                # mirror your promotion's render steps; {stage} → stage name
  - app: web
    kustomize: apps/web/overlays/{stage}
  - app: api
    helm:
      chart: apps/api
      values: apps/api/values-{stage}.yaml
```

Resources are read from `kargo/` and `argocd/` (configurable). Kargo's
`${{ ... }}` expressions are never touched by substitution. All fields:
[docs/config.md](docs/config.md). A complete example:
[examples/oci-flow](examples/oci-flow).

## Commands

```bash
gitops-preflight validate [path...]            # all checks
gitops-preflight validate kargo|steps|argocd|manifests [path...]
gitops-preflight render [path] [--stage prod] [--out dir]   # see what gets validated
gitops-preflight schemas [path...]             # pre-download schemas (offline use)
gitops-preflight init [path]
gitops-preflight doctor
```

## CI

```yaml
- uses: actions/checkout@v4
- uses: shimagrawal/gitops-preflight@v0
  with:
    command: validate
```

The action installs gitops-preflight and flux-schema, caches schemas, and
writes the results to the job summary. Any other CI can use the Docker image.

## Akuity Platform

Akuity-only kinds (`ee.kargo.akuity.io`, e.g. `CustomPromotionStep`) and
enterprise or custom steps have no public schemas. Skip them, or bring your own
schemas: see [docs/akp.md](docs/akp.md).

## What it doesn't check

- What `${{ ... }}` expressions evaluate to (only the structure around them).
- Anything that needs a cluster: whether namespaces, secrets, images or
  repositories exist, admission policies, RBAC.

## Credits

The step-config rules follow `lint-task.py` from
[curated-custom-promotion-tasks](https://github.com/blakepettersson/curated-custom-promotion-tasks).
Schema validation and Kustomize builds are done by
[flux-schema](https://github.com/fluxcd/flux-schema).

## License

[Apache 2.0](LICENSE)
