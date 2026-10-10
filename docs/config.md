# gitops-preflight.yaml

A folder with a `gitops-preflight.yaml` is a project. `gitops-preflight validate`
with no arguments validates every project under the current folder.

```yaml
# Shown in the output. Default: the folder name.
name: oci-flow

# Required. The versions the schemas are pinned to: match what you run.
versions:
  kargo: v1.12.2        # a Kargo release tag (open-source version; v1.12.2-ak.0 → v1.12.2)
  argocd: v3.5.4        # an Argo CD release tag
  kubernetes: 1.36.1    # your cluster's version (`kubectl version`)

# Where the resources live, relative to this file. Defaults shown.
paths:
  kargo: kargo          # Kargo resources: validated against Kargo's CRDs, and
                        # PromotionTasks' step configs against Kargo's step schemas
  argocd: argocd        # Argo CD resources

# ${NAME} placeholders in kargo/ and argocd/ to fill in. Values come from the
# environment, overridden by a .env file (the project's, else the current
# folder's, else --env-file). Only listed names are replaced; Kargo's
# ${{ ... }} expressions are never touched. Keep real credentials out of these
# folders: values are substituted into a temporary copy, never written back.
substitute:
  - GITOPS_REPO_URL
  - OCI_REPO_PREFIX

# Kinds to skip, as Kind or group/version/Kind: kinds without a public schema.
skipKinds:
  - ee.kargo.akuity.io/v1alpha1/CustomPromotionStep

# Steps whose config is not checked: custom steps, enterprise steps.
skipSteps:
  - send-message

# Schemas the open-source catalogs lack (see docs/akp.md).
schemas:
  crds:                 # CRD files or URLs; schemas are extracted from them
    - crds/enterprise-crds.yaml
  steps:                # folders with <step>.json config schemas
    - step-schemas

# Stages to render apps for. Required when render is set.
stages: [dev, test, prod]

# How each app is rendered per stage. Mirror your promotion's render steps so
# you validate what Kargo will produce. {stage} / {STAGE} are replaced with the
# stage name (lower / upper case). Each app has exactly one source:
render:
  - app: nginx
    kustomize: apps/nginx/overlays/{stage}   # a folder with a kustomization
  - app: hello
    helm:
      chart: apps/hello                      # a local chart
      release: hello                         # default: the app name
      namespace: hello-{stage}               # default: "default"
      values: apps/hello/values-{stage}.yaml # one file or a list, merged in order
  - app: raw
    plain: apps/raw/{stage}                  # a YAML file or a folder of them
```

Rendered output uses the layout `<stage>/<app>/manifests.yaml`; see it with
`gitops-preflight render <project> --out <dir>`.

## Schema cache

Schemas are downloaded on first use into `$GITOPS_PREFLIGHT_CACHE`, else
`$XDG_CACHE_HOME/gitops-preflight`, else `~/.cache/gitops-preflight`:

| Folder | From |
|---|---|
| `kargo-<version>/` | Kargo's CRDs (`charts/kargo/resources/crds` of the release) |
| `kargo-steps-<version>/` | Kargo's step schemas (`pkg/promotion/runner/builtin/schemas`) |
| `argocd-<version>/` | Argo CD's Application, ApplicationSet and AppProject CRDs |
| `k8s-<version>/` | Kubernetes' OpenAPI definitions for that release |

A pinned version's sources never change, so cached folders are never refreshed.
Delete one to download it again. `gitops-preflight schemas` downloads ahead of
time, e.g. for offline use.
