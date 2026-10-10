package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Small hand-written step schemas, shaped like Kargo's, so the linter's logic
// is tested independently of any Kargo release.
var testSchemas = map[string]string{
	"tar": `{"type":"object","additionalProperties":false,"required":["inPath","outPath"],
		"properties":{"inPath":{"type":"string","minLength":1},"outPath":{"type":"string","minLength":1},"gzip":{"type":"boolean"}}}`,
	"oci-push": `{"type":"object","additionalProperties":false,"required":["destRef"],
		"properties":{"srcPath":{"type":"string","minLength":1},"destRef":{"type":"string","minLength":1},"mediaType":{"type":"string"}}}`,
	"deploy": `{"type":"object","additionalProperties":false,"required":["apps"],
		"properties":{"apps":{"type":"array","minItems":1,"items":{"$ref":"common.json#/definitions/app"}}}}`,
	"common": `{"definitions":{"app":{"type":"object","additionalProperties":false,"required":["name"],
		"properties":{"name":{"type":"string","minLength":1},"mode":{"enum":["sync","wait"]},
		"revision":{"type":"string","minLength":1},"pin":{"type":"boolean"}},
		"oneOf":[{"properties":{"revision":{"enum":["",null]},"pin":{"enum":[false,null]}}},{"required":["revision"]}]}}}`,
}

func linter(t *testing.T, skip ...string) *Linter {
	t.Helper()
	dir := t.TempDir()
	for name, s := range testSchemas {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sk := map[string]bool{}
	for _, s := range skip {
		sk[s] = true
	}
	return &Linter{Schemas: NewSchemas(dir), Skip: sk}
}

func task(t *testing.T, yaml string) Task {
	t.Helper()
	docs, err := decodeYAMLDocuments([]byte(yaml))
	if err != nil || len(docs) != 1 {
		t.Fatalf("decoding task: %v (%d docs)", err, len(docs))
	}
	return Task(docs[0])
}

const clean = `
apiVersion: kargo.akuity.io/v1alpha1
kind: PromotionTask
metadata: {name: publish}
spec:
  vars:
    - name: repo
      value: registry.example/app
    - name: required
  steps:
    - uses: tar
      as: archive
      config: {inPath: ./out, outPath: ./out.tar.gz, gzip: true}
    - uses: oci-push
      as: push
      config:
        srcPath: ./out.tar.gz
        destRef: ${{ vars.repo }}:${{ vars.required }}
    - uses: deploy
      as: deploy
      config:
        apps:
          - name: app-${{ ctx.stage }}
            revision: ${{ task.outputs.push.digest }}
            pin: true
`

func messages(fs []Finding, warnings bool) []string {
	var out []string
	for _, f := range fs {
		if f.Warning == warnings {
			out = append(out, f.Message)
		}
	}
	return out
}

func TestCleanTask(t *testing.T) {
	if got := linter(t).Lint(task(t, clean)); len(got) != 0 {
		t.Fatalf("clean task has findings: %v", got)
	}
}

func TestRules(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"unknown config key":     {"inPath: ./out,", "inPth: ./out,", `unknown key "inPth"`},
		"missing required key":   {"{inPath: ./out, outPath: ./out.tar.gz, gzip: true}", "{inPath: ./out, gzip: true}", `missing required key "outPath"`},
		"wrong type":             {"gzip: true}", "gzip: 3}", "config.gzip: expected boolean, got number"},
		"interpolated boolean":   {"gzip: true}", `gzip: "x-${{ vars.repo }}"}`, "expected boolean, got string (an expression interpolated into text)"},
		"enum":                   {"pin: true", "pin: true\n            mode: fast", `"fast" is not one of "sync", "wait"`},
		"oneOf none":             {"revision: ${{ task.outputs.push.digest }}", "rev: x", "satisfies none of the 2 oneOf branches"},
		"$ref into sibling file": {"- name: app-${{ ctx.stage }}", "- nam: x", `missing required key "name"`},
		"unknown step":           {"uses: deploy", "uses: deploi", "no schema for this step"},
		"unknown step key":       {"as: archive", "as: archive\n      confg: {}", `unknown step key "confg"`},
		"undeclared var":         {"${{ vars.repo }}:", "${{ vars.rep }}:", "vars.rep is referenced but not declared"},
		"unused var":             {"${{ vars.repo }}:", "x:", `"repo" is declared but never referenced`},
		"empty var default":      {"value: registry.example/app", `value: ""`, "empty default"},
		"forward output":         {"task.outputs.push.digest", "task.outputs.deploy.digest", "task.outputs.deploy names no earlier step"},
		"bare outputs":           {"task.outputs.push.digest", "outputs.push.digest", "`outputs.push` does not resolve inside a task"},
		"nested task":            {"- uses: tar", "- task: {name: other}\n      uses: tar", "cannot reference another task"},
		"retry expression":       {"as: archive", "as: archive\n      retry: {timeout: '${{ vars.repo }}'}", "never expression-templated"},
		"retry duration":         {"as: archive", "as: archive\n      retry: {timeout: 1 hour}", "is not a Go duration"},
		"tar without gzip":       {"gzip: true}", "}", "does not set `gzip: true`"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(clean, tc.from) {
				t.Fatalf("test setup: %q not in the clean task", tc.from)
			}
			got := messages(linter(t).Lint(task(t, strings.Replace(clean, tc.from, tc.to, 1))), false)
			for _, m := range got {
				if strings.Contains(m, tc.want) {
					return
				}
			}
			t.Fatalf("want a finding containing %q, got %q", tc.want, got)
		})
	}
}

func TestMissingAliasIsAWarning(t *testing.T) {
	got := linter(t).Lint(task(t, strings.Replace(clean, "      as: archive\n", "", 1)))
	if errs := messages(got, false); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if w := messages(got, true); len(w) != 1 || !strings.Contains(w[0], "no `as` alias") {
		t.Fatalf("warnings: %v", w)
	}
}

func TestSkipSteps(t *testing.T) {
	src := strings.Replace(clean, "uses: deploy", "uses: custom-step", 1)
	if got := messages(linter(t).Lint(task(t, src)), false); len(got) != 1 {
		t.Fatalf("want 1 'no schema' finding, got %v", got)
	}
	if got := linter(t, "custom-step").Lint(task(t, src)); len(got) != 0 {
		t.Fatalf("skipped step still has findings: %v", got)
	}
}

func TestReadTasksIgnoresOtherKinds(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.yaml")
	content := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: c}\n---\n" + clean
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err := ReadTasks(f)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ReadTasks = %d tasks, %v", len(tasks), err)
	}
}
