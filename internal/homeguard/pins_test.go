package homeguard_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

const pinFilePrelude = `package sample

import (
	"os"
	"os/exec"
	"testing"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/homeguard"
)
`

func parsePinFile(t *testing.T, body string) (*ast.File, map[string]string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "sample_test.go", pinFilePrelude+body, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}
	return file, importPaths(file)
}

func parsePinFiles(t *testing.T, fset *token.FileSet, bodies []string) []*ast.File {
	t.Helper()
	files := make([]*ast.File, 0, len(bodies))
	for i, body := range bodies {
		file, err := parser.ParseFile(fset, fmt.Sprintf("sample%d.go", i), pinFilePrelude+body, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse sample %d: %v", i, err)
		}
		files = append(files, file)
	}
	return files
}

func funcDecl(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("no func %s in sample", name)
	return nil
}

func TestReachesEntrypoint(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"one-line entrypoint", `func TestMain(m *testing.M) { homeguard.Main(m) }`, true},
		{"mainwith wrapper", `func TestMain(m *testing.M) {
	os.Exit(homeguard.MainWith(func() int { return m.Run() }))
}`, true},
		{"conditional re-exec guard before entrypoint", `func TestMain(m *testing.M) {
	if os.Getenv("CHILD") != "" {
		os.Exit(0)
	}
	homeguard.Main(m)
}`, true},
		{"conditional re-exec child exit before entrypoint", `func TestMain(m *testing.M) {
	if os.Getenv("CHILD") != "" {
		homeguard.ChildExit(0)
	}
	homeguard.Main(m)
}`, true},
		{"empty body", `func TestMain(m *testing.M) {}`, false},
		{"earlier return", `func TestMain(m *testing.M) {
	return
	homeguard.Main(m)
}`, false},
		{"labeled return before entrypoint", `func TestMain(m *testing.M) {
	goto L
L:
	return
	homeguard.Main(m)
}`, false},
		{"earlier top-level os.Exit", `func TestMain(m *testing.M) {
	os.Exit(0)
	homeguard.Main(m)
}`, false},
		{"earlier use of m", `func TestMain(m *testing.M) {
	code := m.Run()
	_ = code
	homeguard.Main(m)
}`, false},
		{"dead conditional mention only", `func TestMain(m *testing.M) {
	if false {
		homeguard.Main(m)
	}
}`, false},
		{"shadowing assignment of homeguard", `func TestMain(m *testing.M) {
	homeguard := fakeEntrypoint{}
	homeguard.Main(m)
}`, false},
		{"shadowing var decl of homeguard", `func TestMain(m *testing.M) {
	var homeguard fakeEntrypoint
	homeguard.Main(m)
}`, false},
		{"shadowing assignment of os", `func TestMain(m *testing.M) {
	os := fakeEntrypoint{}
	os.Exit(homeguard.MainWith(m.Run))
}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, imports := parsePinFile(t, tc.body)
			fn := funcDecl(t, file, "TestMain")
			if got := reachesEntrypoint(fn, imports); got != tc.want {
				t.Errorf("reachesEntrypoint = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInitTimeDeriverSites(t *testing.T) {
	matches := initDeriver([]string{"Root", "List"})
	cases := []struct {
		name  string
		files []string
		want  int
	}{
		{"deriver inside plain function", []string{
			`func root() string { home, _ := os.UserHomeDir(); return home }`,
		}, 0},
		{"init calls deriver", []string{
			`func init() { home, _ := os.UserHomeDir(); _ = home }`,
		}, 1},
		{"var initializer calls deriver", []string{
			`var home, _ = os.UserHomeDir()`,
		}, 1},
		{"var initializer reaches deriver through package function", []string{
			`var home = root()

func root() string { home, _ := os.UserHomeDir(); return home }`,
		}, 1},
		{"init reaches deriver through two hops", []string{
			`func init() { _ = outer() }

func outer() string { return inner() }

func inner() string { home, _ := os.UserHomeDir(); return home }`,
		}, 1},
		{"init in one file reaches deriver in another", []string{
			`func init() { _ = root() }`,
			`func root() string { home, _ := os.UserHomeDir(); return home }`,
		}, 1},
		{"init reads HOME from the environment", []string{
			`func init() { _ = os.Getenv("HOME") }`,
		}, 1},
		{"var initializer caches CC_NOTES_HOME", []string{
			`var home, ok = os.LookupEnv("CC_NOTES_HOME")`,
		}, 1},
		{"init reads an unrelated env key", []string{
			`func init() { _ = os.Getenv("PATH") }`,
		}, 0},
		{"variable env key is not followed", []string{
			`var key = "HOME"

func init() { _ = os.Getenv(key) }`,
		}, 0},
		{"var initializer calls ccnhome resolver", []string{
			`var root, _ = ccnhome.Root()`,
		}, 1},
		{"init calls ccnhome resolver", []string{
			`func init() { entries, _ := ccnhome.List(); _ = entries }`,
		}, 1},
		{"init calls unenumerated ccnhome function", []string{
			`func init() { _ = ccnhome.IsGitDir("/") }`,
		}, 0},
		{"function-time ccnhome resolver", []string{
			`func root() (string, error) { return ccnhome.Root() }`,
		}, 0},
		{"stored function literal is not invoked", []string{
			`var derive = func() string { home, _ := os.UserHomeDir(); return home }`,
		}, 0},
		{"immediately invoked function literal", []string{
			`var home = func() string { home, _ := os.UserHomeDir(); return home }()`,
		}, 1},
		{"selector call on a local value", []string{
			`var c = client{}

var home = c.UserHomeDir()`,
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			files := parsePinFiles(t, fset, tc.files)
			if got := initTimeDeriverSites(fset, files, matches); len(got) != tc.want {
				t.Errorf("initTimeDeriverSites = %d site(s) %v, want %d", len(got), got, tc.want)
			}
		})
	}
}

func TestResolverNames(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{"direct, transitive, and env derivers", []string{
			`func Root() (string, error) { home, err := os.UserHomeDir(); return home, err }

func Sub() string { s, _ := sub(); return s }

func sub() (string, error) { return Root() }

func FromEnv() string { return os.Getenv("CC_NOTES_HOME") }

func Hash(s string) string { return s }`,
		}, []string{"FromEnv", "Root", "Sub"}},
		{"methods are not resolvers", []string{
			`func (r repo) Dir() string { home, _ := os.UserHomeDir(); return home }`,
		}, nil},
		{"unexported deriver alone is not listed", []string{
			`func root() string { home, _ := os.UserHomeDir(); return home }`,
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			files := parsePinFiles(t, fset, tc.files)
			if got := resolverNames(files); !slices.Equal(got, tc.want) {
				t.Errorf("resolverNames = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRestoresHome(t *testing.T) {
	imports := map[string]string{"os": "os", "homeguard": homeguardPath}
	cases := []struct {
		name string
		expr string
		want bool
	}{
		{"restore only", `append(os.Environ(), "HOME="+homeguard.RealHome())`, true},
		{"restore then unrelated env", `append(os.Environ(), "HOME="+homeguard.RealHome(), "GOFLAGS=-mod=mod")`, true},
		{"clobber then restore", `append(os.Environ(), "HOME=/nowhere", "HOME="+homeguard.RealHome())`, true},
		{"restore then literal clobber", `append(os.Environ(), "HOME="+homeguard.RealHome(), "HOME=/nowhere")`, false},
		{"restore then concat clobber", `append(os.Environ(), "HOME="+homeguard.RealHome(), "HOME="+os.TempDir())`, false},
		{"no home element", `append(os.Environ(), "GOFLAGS=-mod=mod")`, false},
		{"wrong base", `append(existing, "HOME="+homeguard.RealHome())`, false},
		{"nil", `nil`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := parser.ParseExpr(tc.expr)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.expr, err)
			}
			if got := restoresHome(expr, imports); got != tc.want {
				t.Errorf("restoresHome(%s) = %v, want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestGoExecEnvRestoration(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"restore with no execution", `func helper() {
	cmd := exec.Command("go", "build")
	cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
}`, true},
		{"restore after clobber", `func helper() {
	cmd := exec.Command("go", "build")
	cmd.Env = nil
	cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
}`, true},
		{"later clobber wins", `func helper() {
	cmd := exec.Command("go", "build")
	cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
	cmd.Env = nil
}`, false},
		{"no restore", `func helper() {
	cmd := exec.Command("go", "build")
}`, false},
		{"restore before execution", `func helper() {
	cmd := exec.Command("go", "build")
	cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
	_ = cmd.Run()
}`, true},
		{"restore after execution", `func helper() {
	cmd := exec.Command("go", "build")
	out, _ := cmd.Output()
	_ = out
	cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
}`, false},
		{"restore only on a conditional branch", `func helper(flaky bool) {
	cmd := exec.Command("go", "build")
	if flaky {
		cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
	}
	_ = cmd.Run()
}`, false},
		{"restore before a conditional execution", `func helper(flaky bool) {
	cmd := exec.Command("go", "build")
	cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
	if flaky {
		_ = cmd.Run()
	}
}`, true},
		{"conditional clobber before execution", `func helper(flaky bool) {
	cmd := exec.Command("go", "build")
	cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
	if flaky {
		cmd.Env = nil
	}
	_ = cmd.Run()
}`, false},
		{"clobber after execution is dead config", `func helper() {
	cmd := exec.Command("go", "build")
	cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
	_ = cmd.Run()
	cmd.Env = nil
}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, imports := parsePinFile(t, tc.body)
			commands, envBeforeRun := goExecCommands(funcDecl(t, file, "helper"), imports, map[string]bool{})
			if _, ok := commands["cmd"]; !ok {
				t.Fatal("go exec not detected under name cmd")
			}
			if got := envBeforeRun["cmd"]; got != tc.want {
				t.Errorf("envBeforeRun[cmd] = %v, want %v", got, tc.want)
			}
		})
	}
}
