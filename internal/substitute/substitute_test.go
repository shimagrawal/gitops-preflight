package substitute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyLeavesKargoExpressionsAlone(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("# comment\nOCI_REPO_PREFIX=ghcr.io/x/y\nexport GITOPS_REPO_URL=\"https://g/x.git\"\nOTHER=nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New([]string{"OCI_REPO_PREFIX", "GITOPS_REPO_URL", "UNSET_ONE"}, env)
	if err != nil {
		t.Fatal(err)
	}
	in := "repo: ${OCI_REPO_PREFIX}/app\nurl: ${GITOPS_REPO_URL}\nother: ${OTHER}\n" +
		"expr: ${{ vars.ociRepo }}:${{ ctx.stage }}\nunset: '${UNSET_ONE}'\nplain: $OCI_REPO_PREFIX\n"
	want := "repo: ghcr.io/x/y/app\nurl: https://g/x.git\nother: ${OTHER}\n" +
		"expr: ${{ vars.ociRepo }}:${{ ctx.stage }}\nunset: ''\nplain: $OCI_REPO_PREFIX\n"
	if got := s.Apply(in); got != want {
		t.Fatalf("Apply:\n got %q\nwant %q", got, want)
	}
	if m := s.Missing(); len(m) != 1 || m[0] != "UNSET_ONE" {
		t.Fatalf("Missing = %v, want [UNSET_ONE]", m)
	}
}

func TestEnvironmentIsUsedAndDotenvWins(t *testing.T) {
	t.Setenv("A_VAR", "from-env")
	t.Setenv("B_VAR", "env-b")
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("B_VAR=dotenv-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New([]string{"A_VAR", "B_VAR"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Apply("${A_VAR} ${B_VAR}"); got != "from-env dotenv-b" {
		t.Fatalf("got %q", got)
	}
}

func TestMissingDotenvIsFine(t *testing.T) {
	if _, err := New([]string{"X"}, filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatal(err)
	}
}
