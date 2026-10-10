// Package steps checks the steps of Kargo PromotionTasks and
// ClusterPromotionTasks: each step's config against Kargo's own step schemas
// (which Kargo otherwise only applies at promotion time), and the wiring
// between steps that no schema can express.
//
// Kargo's CRD declares a step's config as "anything", so a CRD-level validator
// can't see a misspelled key inside it; this package can. What it cannot know
// is what a ${{ ... }} expression evaluates to, so for those only the
// structure around the expression is checked.
//
// The rules follow the lint-task.py script in
// github.com/blakepettersson/curated-custom-promotion-tasks.
package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Task is a PromotionTask or ClusterPromotionTask, decoded from YAML/JSON.
type Task map[string]any

// Finding is one problem with a task.
type Finding struct {
	Task    string // task name
	Message string
	// Warning findings are conventions, not errors: Kargo accepts the task.
	Warning bool
}

func (f Finding) String() string { return f.Task + ": " + f.Message }

type findings struct {
	items    []string
	warnings []string
}

func (f *findings) add(msg string)  { f.items = append(f.items, msg) }
func (f *findings) warn(msg string) { f.warnings = append(f.warnings, msg) }

var (
	varRefs = []*regexp.Regexp{
		regexp.MustCompile(`\bvars\.([A-Za-z_]\w*)`),
		regexp.MustCompile(`\bvars\[["']([^"']+)["']\]`),
	}
	taskOutputRefs = []*regexp.Regexp{
		regexp.MustCompile(`\btask\.outputs\.([A-Za-z_][\w-]*)`),
		regexp.MustCompile(`\btask\.outputs\[["']([^"']+)["']\]`),
	}
	// `outputs.x` not preceded by "." (that would be task.outputs.x).
	bareOutputRefs = []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[^.\w])outputs\.([A-Za-z_][\w-]*)`),
		regexp.MustCompile(`(?:^|[^.\w])outputs\[["']([^"']+)["']\]`),
	}
	duration = regexp.MustCompile(`^(\d+(ns|us|µs|ms|s|m|h))+$`)
)

// The keys a promotion step may have (Kargo's PromotionStep type). Only config
// is schema-checked, so a misspelled step-level key would otherwise be ignored.
var stepKeys = map[string]bool{
	"uses": true, "task": true, "as": true, "if": true,
	"continueOnError": true, "retry": true, "vars": true, "config": true,
}

// The layer media type oci-push applies when none is configured.
const defaultLayerMediaType = "application/vnd.oci.image.layer.v1.tar+gzip"

// Linter checks tasks against step schemas.
type Linter struct {
	Schemas *Schemas
	// Steps whose config is not checked (e.g. custom or enterprise steps).
	Skip map[string]bool
}

// Lint returns every finding for t; none means the task is clean.
func (l *Linter) Lint(t Task) []Finding {
	name := stringAt(t, "metadata", "name")
	if name == "" {
		name = "<unnamed>"
	}
	f := &findings{}
	l.lint(t, f)
	out := make([]Finding, 0, len(f.items)+len(f.warnings))
	for _, m := range f.items {
		out = append(out, Finding{Task: name, Message: m})
	}
	for _, m := range f.warnings {
		out = append(out, Finding{Task: name, Message: m, Warning: true})
	}
	return out
}

func (l *Linter) lint(t Task, f *findings) {
	if v, _ := t["apiVersion"].(string); v != "kargo.akuity.io/v1alpha1" {
		f.add(fmt.Sprintf("apiVersion should be kargo.akuity.io/v1alpha1, got %q", v))
	}
	if k, _ := t["kind"].(string); k != "PromotionTask" && k != "ClusterPromotionTask" {
		f.add(fmt.Sprintf("kind should be PromotionTask or ClusterPromotionTask, got %q", k))
	}
	if stringAt(t, "metadata", "name") == "" {
		f.add("metadata.name is required")
	}

	spec, _ := t["spec"].(map[string]any)
	steps, _ := spec["steps"].([]any)
	if len(steps) == 0 {
		f.add("spec.steps must hold at least one step")
	}

	var declared []string
	counts := map[string]int{}
	vars, _ := spec["vars"].([]any)
	for i, raw := range vars {
		v, _ := raw.(map[string]any)
		name, _ := v["name"].(string)
		if name == "" {
			f.add(fmt.Sprintf("spec.vars[%d]: name is required", i))
			continue
		}
		declared = append(declared, name)
		counts[name]++
		// No value means the caller must provide it. value: "" renders as the
		// empty string, which every string field rejects (minLength: 1).
		if val, has := v["value"]; has && val == "" {
			f.add(fmt.Sprintf("spec.vars[%d] (%s): empty default. Omit `value` to require it "+
				"of the caller; an empty string reaches the step as \"\" and is rejected by minLength", i, name))
		}
	}
	for _, name := range sortedCounts(counts) {
		f.add(fmt.Sprintf("spec.vars: %q declared more than once", name))
	}

	var seen []string
	for i, raw := range steps {
		step, _ := raw.(map[string]any)
		at := fmt.Sprintf("spec.steps[%d]", i)
		if _, has := step["task"]; has {
			f.add(at + ": a task step cannot reference another task — Kargo validates " +
				"PromotionTask steps as `has(self.uses) && !has(self.task)`")
		}
		for _, key := range sortedKeys(step) {
			if !stepKeys[key] {
				f.add(fmt.Sprintf("%s: unknown step key %q (a step may have: %s)", at, key, strings.Join(sortedBoolKeys(stepKeys), ", ")))
			}
		}
		checkRetry(step["retry"], at, f)

		uses, _ := step["uses"].(string)
		if uses == "" {
			f.add(at + ": `uses` is required")
			continue
		}
		at = fmt.Sprintf("%s (%s)", at, uses)
		alias, _ := step["as"].(string)
		if alias == "" {
			// Kargo accepts this; without an alias the step's output can't be referenced.
			f.warn(at + ": no `as` alias; give every step one so its output can be referenced")
		}

		// task.outputs.x must name a step that already ran.
		for _, ref := range collectRefs(step, taskOutputRefs) {
			if !contains(seen, ref) {
				known := strings.Join(seen, ", ")
				if known == "" {
					known = "<none>"
				}
				f.add(fmt.Sprintf("%s: task.outputs.%s names no earlier step (aliases so far: %s)", at, ref, known))
			}
		}
		for _, ref := range collectRefs(step, bareOutputRefs) {
			f.add(fmt.Sprintf("%s: `outputs.%s` does not resolve inside a task — use `task.outputs.%s`", at, ref, ref))
		}

		if !l.Skip[uses] {
			schema, err := l.Schemas.Load(uses)
			switch {
			case err != nil:
				f.add(fmt.Sprintf("%s: %v", at, err))
			case schema == nil:
				f.add(at + ": no schema for this step. Either it is not a built-in Kargo step " +
					"(add its schema under schemas.steps, or list it in skipSteps), or the Kargo version has no such step")
			default:
				cfg := step["config"]
				if cfg == nil {
					cfg = map[string]any{}
				}
				sub := &findings{}
				l.Schemas.validate(cfg, schema, schema, "config", sub)
				for _, m := range sub.items {
					f.add(at + ": " + m)
				}
			}
		}
		if alias != "" {
			seen = append(seen, alias)
		}
	}

	checkArchiveMediaTypes(steps, f)

	referenced := collectRefs(steps, varRefs)
	// A var's default may reference an earlier var.
	referenced = append(referenced, collectRefs(vars, varRefs)...)
	for _, name := range uniqueSorted(referenced) {
		if !contains(declared, name) {
			known := strings.Join(declared, ", ")
			if known == "" {
				known = "<none>"
			}
			f.add(fmt.Sprintf("vars.%s is referenced but not declared (declared: %s)", name, known))
		}
	}
	for _, name := range declared {
		if !contains(referenced, name) {
			f.add(fmt.Sprintf("spec.vars: %q is declared but never referenced", name))
		}
	}
}

// checkRetry: retry is a typed field, not part of config, so it is never
// expression-templated; `timeout: ${{ vars.t }}` fails as an unparsable duration.
func checkRetry(raw any, at string, f *findings) {
	if raw == nil {
		return
	}
	retry, ok := raw.(map[string]any)
	if !ok {
		f.add(at + ": `retry` must be an object")
		return
	}
	for _, key := range sortedKeys(retry) {
		if key != "timeout" && key != "errorThreshold" {
			f.add(fmt.Sprintf("%s: unknown retry key %q (retry may have: errorThreshold, timeout)", at, key))
		}
	}
	if timeout, has := retry["timeout"]; has && timeout != nil {
		if isExpr(timeout) {
			f.add(at + ": retry.timeout is a typed duration, not part of `config`, so it " +
				"is never expression-templated. Use a literal such as 1h")
		} else if s, ok := timeout.(string); !ok || !duration.MatchString(s) {
			f.add(fmt.Sprintf("%s: retry.timeout %s is not a Go duration (e.g. 30s, 1h)", at, toJSON(timeout)))
		}
	}
	if th, has := retry["errorThreshold"]; has && th != nil {
		n, ok := th.(float64)
		if !ok || n < 0 || n != float64(int64(n)) {
			f.add(fmt.Sprintf("%s: retry.errorThreshold %s must be a non-negative integer", at, toJSON(th)))
		}
	}
}

// checkArchiveMediaTypes: `tar` defaults to gzip: false while `oci-push`
// defaults its layer media type to ...tar+gzip, so forgetting gzip: true
// publishes a plain tar labelled as gzip. Both steps succeed; Argo CD then
// can't decompress the layer. No schema can see this: it spans two steps.
func checkArchiveMediaTypes(steps []any, f *findings) {
	archives := map[string]bool{}
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		if step["uses"] != "tar" {
			continue
		}
		cfg, _ := step["config"].(map[string]any)
		if out, ok := cfg["outPath"].(string); ok && !isExpr(out) {
			archives[filepath.Clean(out)] = truthy(cfg["gzip"])
		}
	}
	for i, raw := range steps {
		step, _ := raw.(map[string]any)
		if step["uses"] != "oci-push" {
			continue
		}
		cfg, _ := step["config"].(map[string]any)
		src, ok := cfg["srcPath"].(string)
		if !ok || isExpr(src) {
			continue
		}
		gzipped, known := archives[filepath.Clean(src)]
		if !known {
			continue
		}
		mediaType := defaultLayerMediaType
		if mt, ok := cfg["mediaType"].(string); ok {
			mediaType = mt
		}
		if isExpr(mediaType) {
			continue
		}
		claimsGzip := strings.HasSuffix(mediaType, "+gzip")
		at := fmt.Sprintf("spec.steps[%d] (oci-push)", i)
		switch {
		case claimsGzip && !gzipped:
			f.add(fmt.Sprintf("%s: pushes %s as %s but the `tar` step that builds it does not set "+
				"`gzip: true`, so the layer is an uncompressed tar labelled as gzip", at, src, mediaType))
		case gzipped && !claimsGzip:
			f.add(fmt.Sprintf("%s: pushes %s as %s but the `tar` step that builds it sets "+
				"`gzip: true`, so the layer is gzipped and the media type says otherwise", at, src, mediaType))
		}
	}
}

// truthy: whether gzip is on. A wrong type (e.g. a string) is reported by the
// schema check already; treating it as set avoids a second, misleading finding.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	}
	return true
}

// collectRefs matches patterns against every string (keys included) in node.
func collectRefs(node any, patterns []*regexp.Regexp) []string {
	set := map[string]bool{}
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case string:
			for _, p := range patterns {
				for _, m := range p.FindAllStringSubmatch(t, -1) {
					set[m[len(m)-1]] = true
				}
			}
		case map[string]any:
			for k, v := range t {
				walk(k)
				walk(v)
			}
		case []any:
			for _, v := range t {
				walk(v)
			}
		}
	}
	walk(node)
	return uniqueSorted(keysOf(set))
}

func stringAt(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedBoolKeys(m map[string]bool) []string {
	out := keysOf(m)
	sort.Strings(out)
	return out
}

func sortedCounts(m map[string]int) []string {
	var out []string
	for k, n := range m {
		if n > 1 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func uniqueSorted(list []string) []string {
	set := map[string]bool{}
	for _, s := range list {
		set[s] = true
	}
	out := keysOf(set)
	sort.Strings(out)
	return out
}

func sortStrings(s []string) { sort.Strings(s) }

// ReadTasks reads the PromotionTasks and ClusterPromotionTasks from a YAML file
// (other kinds are ignored).
func ReadTasks(path string) ([]Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	docs, err := decodeYAMLDocuments(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var tasks []Task
	for _, d := range docs {
		if k, _ := d["kind"].(string); k == "PromotionTask" || k == "ClusterPromotionTask" {
			tasks = append(tasks, Task(d))
		}
	}
	return tasks, nil
}
