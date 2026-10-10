# Using gitops-preflight with the Akuity Platform

gitops-preflight downloads **open-source** schemas: Kargo's and Argo CD's CRDs
and Kargo's built-in step schemas. Pin the open-source version your Akuity
instance is built on: Kargo `v1.12.2-ak.0` → `versions.kargo: v1.12.2`.

Some things on the Akuity Platform have no public schema:

| What | Example | Without configuration |
|---|---|---|
| Akuity-only kinds (`ee.kargo.akuity.io`) | `CustomPromotionStep`, `MessageChannel`, `ClusterMessageChannel`, `EventRouter` | `✗ … schema not found` |
| Enterprise steps | `send-message`, `snow-create`, `snow-update`, … | `no schema for this step` |
| Your custom steps | `uses: kyverno-validate` | `no schema for this step` |

Handle each with one of these options.

## 1. Skip them

They're listed as skipped, not failed.

```yaml
skipKinds:
  - ee.kargo.akuity.io/v1alpha1/CustomPromotionStep
  - ee.kargo.akuity.io/v1alpha1/MessageChannel
skipSteps:
  - send-message
  - snow-create
  - snow-update
```

## 2. Bring the CRDs

If you have the CRD files for those kinds, schemas are extracted from them and
the resources are validated like any other:

```yaml
schemas:
  crds:
    - crds/akuity-enterprise-crds.yaml     # file (relative to the project) or URL
```

## 3. Bring step schemas

Write a JSON Schema for a step's `config:` and point to its folder. The file is
named after the step: `uses: kyverno-smoke-test` → `kyverno-smoke-test.json`.

```yaml
schemas:
  steps:
    - step-schemas
```

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["namespace"],
  "properties": {
    "namespace": { "type": "string", "minLength": 1 }
  }
}
```

Project schemas take precedence over Kargo's built-in ones. Expressions
(`${{ ... }}`) are handled as for built-in steps: only the structure around
them is checked.
