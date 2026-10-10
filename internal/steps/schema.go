package steps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A ${{ ... }} expression.
var exprRe = regexp.MustCompile(`(?s)\$\{\{.*?\}\}`)
var wholeExprRe = regexp.MustCompile(`(?s)^\$\{\{.*?\}\}$`)

// Placeholder of each JSON Schema type, substituted for an expression so the
// structure around it can still be validated.
type stringPlaceholder struct{}

var placeholders = map[string]any{
	"string":  stringPlaceholder{},
	"boolean": true,
	"number":  1.0,
	"integer": 1.0,
	"array":   []any{},
	"object":  map[string]any{},
	"null":    nil,
}

// Schemas loads step config schemas by step name from one or more folders
// (earlier folders win), with $ref resolution.
type Schemas struct {
	dirs  []string
	cache map[string]map[string]any
}

// NewSchemas looks for <step>.json in dirs, in order.
func NewSchemas(dirs ...string) *Schemas {
	return &Schemas{dirs: dirs, cache: map[string]map[string]any{}}
}

// Load returns the schema for name, or nil when no folder has one.
func (s *Schemas) Load(name string) (map[string]any, error) {
	if sc, ok := s.cache[name]; ok {
		return sc, nil
	}
	for _, d := range s.dirs {
		data, err := os.ReadFile(filepath.Join(d, name+".json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var sc map[string]any
		if err := json.Unmarshal(data, &sc); err != nil {
			return nil, fmt.Errorf("%s.json: %w", name, err)
		}
		s.cache[name] = sc
		return sc, nil
	}
	s.cache[name] = nil
	return nil, nil
}

// resolve follows a local (#/definitions/x) or sibling-file
// (argocd-common.json#/definitions/x) $ref.
func (s *Schemas) resolve(ref string, current map[string]any) (map[string]any, map[string]any) {
	file, pointer, _ := strings.Cut(ref, "#")
	root := current
	if file != "" {
		r, err := s.Load(strings.TrimSuffix(file, ".json"))
		if err != nil || r == nil {
			return nil, nil
		}
		root = r
	}
	var node any = root
	for _, token := range strings.Split(strings.Trim(pointer, "/"), "/") {
		if token == "" {
			continue
		}
		m, ok := node.(map[string]any)
		if !ok {
			return nil, nil
		}
		if node, ok = m[token]; !ok {
			return nil, nil
		}
	}
	m, ok := node.(map[string]any)
	if !ok {
		return nil, nil
	}
	return m, root
}

func isExpr(v any) bool {
	s, ok := v.(string)
	return ok && exprRe.MatchString(s)
}

// isWholeExpr: nothing but one expression, which Kargo replaces with the
// expression's typed result. Text around an expression always yields a string.
func isWholeExpr(v any) bool {
	s, ok := v.(string)
	return ok && wholeExprRe.MatchString(strings.TrimSpace(s))
}

func containsExpr(v any) bool {
	switch t := v.(type) {
	case string:
		return isExpr(t)
	case map[string]any:
		for _, x := range t {
			if containsExpr(x) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if containsExpr(x) {
				return true
			}
		}
	}
	return false
}

func schemaTypes(schema map[string]any) []string {
	switch t := schema["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// coerce swaps an expression for a placeholder of a type the schema accepts.
// checkable is false when the real value is unknowable, so value-level
// keywords (enum, minLength, pattern) are skipped.
func coerce(v any, schema map[string]any) (any, bool) {
	if !isExpr(v) {
		return v, true
	}
	types := schemaTypes(schema)
	if types == nil {
		types = []string{"string"}
	}
	if isWholeExpr(v) {
		for _, t := range types {
			if p, ok := placeholders[t]; ok {
				return p, false
			}
		}
	}
	// Interpolation into text always yields a string; if the schema doesn't
	// accept one, the type check below reports it.
	return v, false
}

func typeMatches(v any, types []string) bool {
	for _, t := range types {
		switch t {
		case "string":
			switch v.(type) {
			case string, stringPlaceholder:
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "number", "integer":
			if _, ok := v.(float64); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		}
	}
	return false
}

func describe(v any) string {
	switch t := v.(type) {
	case bool:
		return "boolean"
	case string:
		if isExpr(t) {
			return "string (an expression interpolated into text)"
		}
		return "string"
	case stringPlaceholder:
		return "string"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func toJSON(v any) string {
	if _, ok := v.(stringPlaceholder); ok {
		return `"<expression>"`
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// validate checks value against schema, implementing the keywords Kargo's
// step schemas use: $ref, type, properties, additionalProperties, required,
// items, minItems, minLength, minProperties, enum, pattern, oneOf, anyOf, not.
func (s *Schemas) validate(value any, schema, root map[string]any, path string, f *findings) {
	if ref, ok := schema["$ref"].(string); ok {
		target, targetRoot := s.resolve(ref, root)
		if target == nil {
			f.add(fmt.Sprintf("%s: unresolvable $ref %q", orRoot(path), ref))
			return
		}
		s.validate(value, target, targetRoot, path, f)
		return
	}

	value, checkable := coerce(value, schema)
	label := orRoot(path)

	if types := schemaTypes(schema); types != nil && !typeMatches(value, types) {
		f.add(fmt.Sprintf("%s: expected %s, got %s", label, strings.Join(types, " or "), describe(value)))
		return
	}

	if checkable {
		if enum, ok := schema["enum"].([]any); ok && !inEnum(value, enum) {
			allowed := make([]string, len(enum))
			for i, e := range enum {
				allowed[i] = toJSON(e)
			}
			f.add(fmt.Sprintf("%s: %s is not one of %s", label, toJSON(value), strings.Join(allowed, ", ")))
		}
		if str, ok := value.(string); ok {
			if min, ok := schema["minLength"].(float64); ok && float64(len([]rune(str))) < min {
				f.add(fmt.Sprintf("%s: must be at least %d character(s)", label, int(min)))
			}
			if pat, ok := schema["pattern"].(string); ok {
				if re, err := regexp.Compile(pat); err == nil && !re.MatchString(str) {
					f.add(fmt.Sprintf("%s: %q does not match %s", label, str, pat))
				}
			}
		}
	}

	if obj, ok := value.(map[string]any); ok {
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, k := range req {
				if key, ok := k.(string); ok {
					if _, present := obj[key]; !present {
						f.add(fmt.Sprintf("%s: missing required key %q", label, key))
					}
				}
			}
		}
		if ap, ok := schema["additionalProperties"].(bool); ok && !ap {
			for _, key := range sortedKeys(obj) {
				if _, known := props[key]; !known {
					known := strings.Join(sortedKeys(props), ", ")
					if known == "" {
						known = "<none>"
					}
					f.add(fmt.Sprintf("%s: unknown key %q (schema defines: %s)", label, key, known))
				}
			}
		}
		if min, ok := schema["minProperties"].(float64); ok && float64(len(obj)) < min {
			f.add(fmt.Sprintf("%s: must have at least %d key(s)", label, int(min)))
		}
		for _, key := range sortedKeys(props) {
			if v, present := obj[key]; present {
				sub, _ := props[key].(map[string]any)
				child := key
				if path != "" {
					child = path + "." + key
				}
				s.validate(v, sub, root, child, f)
			}
		}
	}

	if arr, ok := value.([]any); ok {
		if min, ok := schema["minItems"].(float64); ok && float64(len(arr)) < min {
			f.add(fmt.Sprintf("%s: must have at least %d item(s)", label, int(min)))
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range arr {
				s.validate(item, items, root, fmt.Sprintf("%s[%d]", label, i), f)
			}
		}
	}

	for _, keyword := range []string{"oneOf", "anyOf"} {
		branches, ok := schema[keyword].([]any)
		if !ok {
			continue
		}
		passing := 0
		for _, b := range branches {
			if bs, ok := b.(map[string]any); ok && len(s.branchErrors(value, bs, root)) == 0 {
				passing++
			}
		}
		switch {
		case passing == 0:
			f.add(fmt.Sprintf("%s: satisfies none of the %d %s branches", label, len(branches), keyword))
		case keyword == "oneOf" && passing > 1 && !containsExpr(value):
			// Several branches matching is only an error when every value is a
			// literal: with an expression, value-discriminated branches (e.g.
			// argocd-update's desiredRevision) can't be told apart here.
			f.add(fmt.Sprintf("%s: matches %d of %d mutually exclusive oneOf branches", label, passing, len(branches)))
		}
	}

	if not, ok := schema["not"].(map[string]any); ok && len(s.branchErrors(value, not, root)) == 0 {
		f.add(fmt.Sprintf("%s: must not match the schema's `not` clause", label))
	}
}

func (s *Schemas) branchErrors(value any, schema, root map[string]any) []string {
	probe := &findings{}
	s.validate(value, schema, root, "", probe)
	return probe.items
}

func inEnum(v any, enum []any) bool {
	for _, e := range enum {
		if toJSON(e) == toJSON(v) {
			return true
		}
	}
	return false
}

func orRoot(path string) string {
	if path == "" {
		return "<root>"
	}
	return path
}
