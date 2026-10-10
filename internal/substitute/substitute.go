// Package substitute fills in ${NAME} placeholders from an allowlist.
//
// Unlike envsubst (or Flux's post-build substitution), only the listed names
// are replaced, and nothing else that starts with "$" is interpreted. That
// leaves Kargo's ${{ ... }} expressions exactly as written.
package substitute

import (
	"bufio"
	"errors"
	"os"
	"regexp"
	"strings"
)

var placeholder = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Substituter replaces allowed placeholders with values.
type Substituter struct {
	allowed map[string]bool
	values  map[string]string
}

// New allows the given names. Values come from the environment, overridden by
// the dotenv file at envFile when it exists.
func New(names []string, envFile string) (*Substituter, error) {
	s := &Substituter{allowed: map[string]bool{}, values: map[string]string{}}
	for _, n := range names {
		s.allowed[n] = true
		if v, ok := os.LookupEnv(n); ok {
			s.values[n] = v
		}
	}
	if envFile != "" {
		file, err := ReadDotenv(envFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for k, v := range file {
			if s.allowed[k] {
				s.values[k] = v
			}
		}
	}
	return s, nil
}

// Apply replaces every allowed ${NAME} in text. Allowed names without a value
// become empty, as envsubst does; Missing reports them.
func (s *Substituter) Apply(text string) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		name := m[2 : len(m)-1]
		if !s.allowed[name] {
			return m
		}
		return s.values[name]
	})
}

// Missing lists allowed names that have no value.
func (s *Substituter) Missing() []string {
	var missing []string
	for n := range s.allowed {
		if _, ok := s.values[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing
}

// ReadDotenv parses KEY=VALUE lines. Blank lines and # comments are ignored;
// an optional "export " prefix and surrounding quotes are stripped.
func ReadDotenv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	values := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		values[k] = v
	}
	return values, sc.Err()
}
