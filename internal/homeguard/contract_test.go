package homeguard_test

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/homeguard"
)

// THREAT MODEL — read before adding pins. This guard catches accidents, not
// adversaries: it exists so a developer who forgets an entrypoint, drops an
// Env restore, or adds a package reaching a per-user state root gets a red
// run instead of a polluted home. It is a smoke alarm, and it is not a proof.
//
// The pins are syntactic AST matching without type resolution, so their reach
// stops at what a parse tree shows. They do not follow a call through a
// generic function, a method or interface, or a wrapper package; they read an
// env key only as a string literal, so os.Getenv(ccnhome.Env) reads green; and
// the init-time deriver set enumerates ccnhome's resolvers, so an in-module
// resolver elsewhere — helperclient.InstalledDir, helperapp.RuntimeDirectory —
// is invisible to it. Those blind spots are ACCIDENT-shaped, not merely
// adversarial: ordinary code reaches every one of them. They are known,
// measured, and accepted for now, not silently absent.
//
// Closing them needs a type-resolved module-wide call graph (go/types via
// golang.org/x/tools), which is a larger change than the guard itself and is
// only worth building if one of these blind spots actually bites. Until then
// the runtime footprint diff is the real backstop for anything the pins miss
// after homeguard's init runs. Deliberate evasions are a separate matter and
// stay declared in the design doc's WHAT THE GUARD DOES NOT CATCH list.

const (
	modulePath    = "github.com/yasyf/cc-notes"
	homeguardPath = modulePath + "/internal/homeguard"
	ccnhomePath   = modulePath + "/internal/ccnhome"
)

var rootDeps = []string{
	ccnhomePath,
	"github.com/yasyf/daemonkit/service",
}

var homeDerivers = map[string][]string{
	"os":      {"UserHomeDir", "UserConfigDir", "UserCacheDir"},
	"os/user": {"Current"},
}

var homeEnvKeys = []string{"HOME", "CC_NOTES_HOME", "DAEMONKIT_HOME"}

var homeDeriverAllowlist = []string{
	ccnhomePath,
	modulePath + "/internal/cli",
	modulePath + "/internal/helperapp",
	modulePath + "/internal/helperclient",
}

// IgnoredGoFiles is deliberately absent: the pins must see only what builds for
// the host GOOS/GOARCH under the default tag set, or a TestMain the platform
// excludes satisfies a pin the package's real test binary fails.
type listPackage struct {
	ImportPath   string
	Dir          string
	Deps         []string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
}

func (p listPackage) sourceFiles() []string {
	return p.join(slices.Concat(p.GoFiles, p.CgoFiles))
}

func (p listPackage) testFiles() []string {
	return p.join(slices.Concat(p.TestGoFiles, p.XTestGoFiles))
}

func (p listPackage) join(files []string) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, filepath.Join(p.Dir, file))
	}
	return paths
}

// TestHomeDisciplineContract pins the obligations that keep a test run out of
// the developer's real home: every test binary that can reach a per-user
// state root routes its entrypoint through homeguard, home derivation happens
// only in the packages the allowlist permits, no package in the module
// derives a home at package-initialization time, and every test that execs
// the go tool restores the real HOME for it before the command runs. All the
// checks read one `go list -test -json ./...` pass, so all see exactly the
// files that build for the host platform.
func TestHomeDisciplineContract(t *testing.T) {
	pkgs := listPackages(t)
	base := map[string]listPackage{}
	for _, pkg := range pkgs {
		if strings.HasPrefix(pkg.ImportPath, modulePath) &&
			!strings.HasSuffix(pkg.ImportPath, ".test") &&
			!strings.Contains(pkg.ImportPath, " [") {
			base[pkg.ImportPath] = pkg
		}
	}
	guarded := map[string]bool{}
	for path, pkg := range base {
		guarded[path] = declaresEntrypoint(t, pkg)
	}

	t.Run("deps", func(t *testing.T) {
		var missing []string
		for _, pkg := range pkgs {
			path, found := strings.CutSuffix(pkg.ImportPath, ".test")
			if !found || guarded[path] {
				continue
			}
			for _, dep := range rootDeps {
				if slices.Contains(pkg.Deps, dep) {
					missing = append(missing, path+" (reaches "+dep+")")
					break
				}
			}
		}
		reportMissing(t, missing, "test binaries reaching a per-user state root")
	})

	// Test files are scanned on purpose. The deps pin forces an entrypoint
	// only on binaries reaching a per-user state root through the import
	// graph, so a test file deriving a home through the stdlib alone runs
	// against the real home with no redirect — and os/user.Current resolves
	// the passwd home no redirect can move, in guarded packages too. The
	// accepted costs: a test asserting a real home value reads
	// os.Getenv("HOME") or homeguard.RealHome() instead of a deriver, and an
	// allowlist entry held up only by a test-file site is not reported stale,
	// since it still names a covered call site.
	t.Run("call sites", func(t *testing.T) {
		var outside, unguarded, stale []string
		for path, pkg := range base {
			sites := homeDeriverSites(t, slices.Concat(pkg.sourceFiles(), pkg.testFiles()))
			if !slices.Contains(homeDeriverAllowlist, path) {
				for _, site := range sites {
					outside = append(outside, path+" ("+site+")")
				}
				continue
			}
			if len(sites) == 0 {
				stale = append(stale, path)
				continue
			}
			if !guarded[path] {
				unguarded = append(unguarded, path)
			}
		}
		if len(outside) > 0 {
			slices.Sort(outside)
			t.Errorf("%d home-deriver call site(s) sit in packages not permitted to derive a home:\n  %s\n\n%s",
				len(outside), strings.Join(outside, "\n  "),
				"Deriving a per-user home is internal/ccnhome's job; route the lookup\n"+
					"through it, or — for a genuinely new audited deriver — add the package to\n"+
					"homeDeriverAllowlist and declare the homeguard entrypoint in the same change.\n"+
					"An entrypoint alone does not exempt a package: isolation and permission\n"+
					"to derive are different obligations, and os/user.Current resolves the\n"+
					"passwd home no environment redirect can move.")
		}
		if len(stale) > 0 {
			slices.Sort(stale)
			t.Errorf("%d allowlisted package(s) no longer contain a home-deriver call site:\n  %s\n\n%s",
				len(stale), strings.Join(stale, "\n  "),
				"Delete the stale homeDeriverAllowlist entries: a stale entry quietly\n"+
					"re-permits derivation the next time such a call appears there.")
		}
		reportMissing(t, unguarded, "packages permitted to derive a home")
	})

	t.Run("init time", func(t *testing.T) {
		resolvers := rootResolvers(t, base[ccnhomePath])
		if len(resolvers) == 0 {
			t.Fatal("internal/ccnhome no longer contains an exported root resolver; the init-time pin's in-module deriver set would be empty — re-derive it before trusting this pin")
		}
		matches := initDeriver(resolvers)
		var violations []string
		for path, pkg := range base {
			if path == homeguardPath {
				continue
			}
			fset := token.NewFileSet()
			for _, group := range [][]string{
				slices.Concat(pkg.sourceFiles(), pkg.join(pkg.TestGoFiles)),
				pkg.join(pkg.XTestGoFiles),
			} {
				var files []*ast.File
				for _, file := range group {
					files = append(files, parseFile(t, fset, file))
				}
				for _, site := range initTimeDeriverSites(fset, files, matches) {
					violations = append(violations, path+" ("+site+")")
				}
			}
		}
		if len(violations) == 0 {
			return
		}
		slices.Sort(violations)
		t.Fatalf("%d home-deriver call site(s) are reachable at package initialization time:\n  %s\n\n%s",
			len(violations), strings.Join(violations, "\n  "),
			"No package in this module derives a per-user home at package\n"+
				"initialization time — not in an init function, not in a package-level\n"+
				"var initializer, not transitively through bare calls or an immediately\n"+
				"invoked literal. Go orders homeguard's init-time redirect only before\n"+
				"packages that import it; everything else initializes in import-path\n"+
				"order, so an init-time derivation can resolve the real home before the\n"+
				"redirect exists, and an init-time write is baked into the footprint's\n"+
				"before snapshot and reports green. This rule is module-wide and governs\n"+
				"WHEN a home may be derived; homeDeriverAllowlist governs WHERE — an\n"+
				"allowlisted package still derives homes only inside functions its\n"+
				"callers invoke at run time. Move the derivation behind such a function;\n"+
				"a test that needs the pre-redirect HOME calls homeguard.RealHome().")
	})

	t.Run("toolchain env", func(t *testing.T) {
		var files []string
		for _, pkg := range base {
			files = append(files, pkg.testFiles()...)
		}
		slices.Sort(files)
		violations := goExecSitesWithoutEnv(t, slices.Compact(files))
		if len(violations) == 0 {
			return
		}
		slices.Sort(violations)
		t.Fatalf("%d test call site(s) exec the go tool without restoring HOME in Env before the command runs:\n  %s\n\n%s",
			len(violations), strings.Join(violations, "\n  "),
			"homeguard redirects HOME at package init, and the go tool derives GOPATH,\n"+
				"GOMODCACHE, GOCACHE, and GOENV from it, so a subprocess inheriting the\n"+
				"redirect resolves an empty module cache and an empty build cache: the build\n"+
				"re-downloads the module graph online and fails outright offline. The pin\n"+
				"accepts exactly\n"+
				`cmd.Env = append(os.Environ(), "HOME="+homeguard.RealHome(), ...)`+"\n"+
				"as the LAST Env assignment before the command's first\n"+
				"Run/Start/Output/CombinedOutput call, with no later \"HOME=\" element\n"+
				"inside the append — os/exec resolves duplicate keys last-wins — and made\n"+
				"unconditionally: an assignment only on a conditional branch, or after the\n"+
				"command already ran, does not count.")
	})
}

func reportMissing(t *testing.T, missing []string, subject string) {
	t.Helper()
	if len(missing) == 0 {
		return
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	t.Fatalf("%d %s do not declare a homeguard entrypoint:\n  %s\n\n%s",
		len(missing), subject, strings.Join(missing, "\n  "),
		"Each needs an IN-PACKAGE test file (package <pkg>, not <pkg>_test) declaring:\n"+
			"\tfunc TestMain(m *testing.M) { homeguard.Main(m) }\n"+
			"An entrypoint with its own setup and teardown ends in\n"+
			"\tos.Exit(homeguard.MainWith(func() int { ... }))\n"+
			"The in-package import is load-bearing: it orders homeguard's init-time home\n"+
			"redirect before every initializer of the package under test, which an\n"+
			"external _test package's import does not.\n"+
			"The entrypoint call must be TestMain's final statement, with no return,\n"+
			"os.Exit, use of m, or shadowing of its qualifiers before it: a call on a\n"+
			"dead path does not count.\n"+
			"A file the host platform's build constraints exclude does not count.")
}

func listPackages(t *testing.T) []listPackage {
	t.Helper()
	list := exec.Command("go", "list", "-test", "-json", "./...")
	list.Dir = moduleRoot(t)
	list.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
	var stderr bytes.Buffer
	list.Stderr = &stderr
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list -test -json ./...: %v\n%s", err, stderr.String())
	}
	var pkgs []listPackage
	decoder := json.NewDecoder(bytes.NewReader(out))
	for decoder.More() {
		var pkg listPackage
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// declaresEntrypoint accepts a TestMain only from the package's own test
// files (TestGoFiles): the in-package import of homeguard is what orders its
// init-time redirect before the package's variable initializers and init
// functions, while an external _test package's import orders homeguard only
// before that _test package — otherwise-unrelated packages initialize in
// import-path order, and every allowlisted package sorts before
// internal/homeguard (verified by execution against Go 1.26).
func declaresEntrypoint(t *testing.T, pkg listPackage) bool {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range pkg.join(pkg.TestGoFiles) {
		file := parseFile(t, fset, path)
		imports := importPaths(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" {
				continue
			}
			if reachesEntrypoint(fn, imports) {
				return true
			}
		}
	}
	return false
}

// reachesEntrypoint accepts only TestMain bodies whose final statement is
// homeguard.Main(m) or os.Exit(homeguard.MainWith(...)) and whose earlier
// top-level statements cannot run the tests or end the function first: none
// may return (labels unwrapped; go vet's unreachable check backstops the code
// after one), call os.Exit, or use m, and a final call whose qualifier the
// body declares or assigns is rejected as shadowed. A mention on a dead or
// conditional path does not count. Earlier conditional statements that exit
// some other way stay legal (fusefs guards a re-exec child that way) — since
// the redirect lives in homeguard's package init, such a shape can skip only
// the footprint diff, never the redirect.
func reachesEntrypoint(fn *ast.FuncDecl, imports map[string]string) bool {
	stmts := fn.Body.List
	if len(stmts) == 0 {
		return false
	}
	param := ""
	if fields := fn.Type.Params.List; len(fields) == 1 && len(fields[0].Names) == 1 {
		param = fields[0].Names[0].Name
	}
	for _, stmt := range stmts[:len(stmts)-1] {
		for {
			labeled, ok := stmt.(*ast.LabeledStmt)
			if !ok {
				break
			}
			stmt = labeled.Stmt
		}
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return false
		}
		if expr, ok := stmt.(*ast.ExprStmt); ok {
			if call, ok := expr.X.(*ast.CallExpr); ok && isPkgCall(call, imports, "os", "Exit") {
				return false
			}
		}
		if param != "" && mentions(stmt, param) {
			return false
		}
	}
	bound := boundIdents(fn.Body)
	expr, ok := stmts[len(stmts)-1].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	if unshadowedPkgCall(call, imports, bound, homeguardPath, "Main") {
		return true
	}
	if !unshadowedPkgCall(call, imports, bound, "os", "Exit") || len(call.Args) != 1 {
		return false
	}
	wrapped, ok := call.Args[0].(*ast.CallExpr)
	return ok && unshadowedPkgCall(wrapped, imports, bound, homeguardPath, "MainWith")
}

func unshadowedPkgCall(call *ast.CallExpr, imports map[string]string, bound map[string]bool, path, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && imports[pkg.Name] == path && !bound[pkg.Name]
}

func boundIdents(body *ast.BlockStmt) map[string]bool {
	bound := map[string]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			for _, target := range node.Lhs {
				if ident, ok := target.(*ast.Ident); ok {
					bound[ident.Name] = true
				}
			}
		case *ast.ValueSpec:
			for _, name := range node.Names {
				bound[name.Name] = true
			}
		case *ast.RangeStmt:
			if node.Tok == token.DEFINE {
				if ident, ok := node.Key.(*ast.Ident); ok {
					bound[ident.Name] = true
				}
				if ident, ok := node.Value.(*ast.Ident); ok {
					bound[ident.Name] = true
				}
			}
		}
		return true
	})
	return bound
}

func mentions(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && ident.Name == name {
			found = true
		}
		return !found
	})
	return found
}

type deriverMatcher func(call *ast.CallExpr, imports map[string]string) bool

func pkgSelector(call *ast.CallExpr, imports map[string]string) (path, name string, ok bool) {
	sel, selOK := call.Fun.(*ast.SelectorExpr)
	if !selOK {
		return "", "", false
	}
	pkg, identOK := sel.X.(*ast.Ident)
	if !identOK {
		return "", "", false
	}
	return imports[pkg.Name], sel.Sel.Name, true
}

func stdlibDeriver(call *ast.CallExpr, imports map[string]string) bool {
	path, name, ok := pkgSelector(call, imports)
	return ok && slices.Contains(homeDerivers[path], name)
}

func homeEnvRead(call *ast.CallExpr, imports map[string]string) bool {
	path, name, ok := pkgSelector(call, imports)
	if !ok || path != "os" || (name != "Getenv" && name != "LookupEnv") || len(call.Args) != 1 {
		return false
	}
	key, literal := literalString(call.Args[0])
	return literal && slices.Contains(homeEnvKeys, key)
}

// initDeriver widens the deriver set for the init-time pin: beyond the
// stdlib derivers, caching one of the three redirect keys from the
// environment and calling one of internal/ccnhome's enumerated root
// resolvers each derive a home just as surely.
func initDeriver(resolvers []string) deriverMatcher {
	return func(call *ast.CallExpr, imports map[string]string) bool {
		if stdlibDeriver(call, imports) || homeEnvRead(call, imports) {
			return true
		}
		path, name, ok := pkgSelector(call, imports)
		return ok && path == ccnhomePath && slices.Contains(resolvers, name)
	}
}

func homeDeriverSites(t *testing.T, files []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var sites []string
	for _, path := range files {
		file := parseFile(t, fset, path)
		imports := importPaths(file)
		ast.Inspect(file, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok && stdlibDeriver(call, imports) {
				sites = append(sites, fset.Position(call.Pos()).String())
			}
			return true
		})
	}
	return sites
}

type scope struct {
	body    ast.Node
	imports map[string]string
}

type initRoot struct {
	scope
	pos token.Pos
}

func packageFuncs(files []*ast.File) map[string]scope {
	funcs := map[string]scope{}
	for _, file := range files {
		imports := importPaths(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Body != nil && fn.Name.Name != "init" {
				funcs[fn.Name.Name] = scope{fn.Body, imports}
			}
		}
	}
	return funcs
}

func initRoots(files []*ast.File) []initRoot {
	var roots []initRoot
	for _, file := range files {
		imports := importPaths(file)
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Recv == nil && decl.Body != nil && decl.Name.Name == "init" {
					roots = append(roots, initRoot{scope{decl.Body, imports}, decl.Pos()})
				}
			case *ast.GenDecl:
				if decl.Tok != token.VAR {
					continue
				}
				for _, spec := range decl.Specs {
					for _, value := range spec.(*ast.ValueSpec).Values {
						roots = append(roots, initRoot{scope{value, imports}, value.Pos()})
					}
				}
			}
		}
	}
	return roots
}

// deriverReach reports the matching calls reachable from root: directly,
// transitively through bare calls to the package's own functions, and
// through immediately invoked function literals. A function literal that is
// only stored, a method call, and a stored function value are not followed —
// residue every deriver rule shares.
func deriverReach(root scope, funcs map[string]scope, matches deriverMatcher) []token.Pos {
	var sites []token.Pos
	seen := map[string]bool{}
	var walk func(s scope)
	walk = func(s scope) {
		invoked := invokedFuncLits(s.body)
		ast.Inspect(s.body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncLit:
				return invoked[node]
			case *ast.CallExpr:
				if matches(node, s.imports) {
					sites = append(sites, node.Pos())
				}
				if fun, ok := node.Fun.(*ast.Ident); ok {
					if next, found := funcs[fun.Name]; found && !seen[fun.Name] {
						seen[fun.Name] = true
						walk(next)
					}
				}
			}
			return true
		})
	}
	walk(root)
	return sites
}

// initTimeDeriverSites reports matching calls reachable at package
// initialization time — from an init function or a package-level var
// initializer. Initialization order between unrelated packages is a
// toolchain detail, so the rule holds regardless of where a package sorts
// relative to homeguard.
func initTimeDeriverSites(fset *token.FileSet, files []*ast.File, matches deriverMatcher) []string {
	funcs := packageFuncs(files)
	var sites []string
	for _, root := range initRoots(files) {
		for _, pos := range deriverReach(root.scope, funcs, matches) {
			sites = append(sites, fset.Position(root.pos).String()+" reaches "+fset.Position(pos).String())
		}
	}
	return sites
}

func rootResolvers(t *testing.T, pkg listPackage) []string {
	t.Helper()
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(pkg.sourceFiles()))
	for _, path := range pkg.sourceFiles() {
		files = append(files, parseFile(t, fset, path))
	}
	return resolverNames(files)
}

// resolverNames enumerates the exported functions that resolve a per-user
// root: those reaching a stdlib home deriver or a home env read through
// deriverReach. Run against internal/ccnhome it derives the init-time pin's
// in-module deriver set from the source instead of a hardcoded guess.
func resolverNames(files []*ast.File) []string {
	funcs := packageFuncs(files)
	matches := func(call *ast.CallExpr, imports map[string]string) bool {
		return stdlibDeriver(call, imports) || homeEnvRead(call, imports)
	}
	var names []string
	for name, fn := range funcs {
		if ast.IsExported(name) && len(deriverReach(fn, funcs, matches)) > 0 {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func invokedFuncLits(node ast.Node) map[*ast.FuncLit]bool {
	invoked := map[*ast.FuncLit]bool{}
	ast.Inspect(node, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if lit, ok := call.Fun.(*ast.FuncLit); ok {
				invoked[lit] = true
			}
		}
		return true
	})
	return invoked
}

func goExecSitesWithoutEnv(t *testing.T, files []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(files))
	goNames := map[string]bool{}
	for _, path := range files {
		file := parseFile(t, fset, path)
		parsed = append(parsed, file)
		collectGoNames(file, goNames)
	}
	var violations []string
	for _, file := range parsed {
		imports := importPaths(file)
		for _, decl := range file.Decls {
			commands, envBeforeRun := goExecCommands(decl, imports, goNames)
			for name, pos := range commands {
				if !envBeforeRun[name] {
					violations = append(violations, fset.Position(pos).String())
				}
			}
		}
	}
	return violations
}

func collectGoNames(file *ast.File, names map[string]bool) {
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if i < len(node.Values) && isGoString(node.Values[i]) {
					names[name.Name] = true
				}
			}
		case *ast.AssignStmt:
			for i, target := range node.Lhs {
				ident, ok := target.(*ast.Ident)
				if ok && i < len(node.Rhs) && isGoString(node.Rhs[i]) {
					names[ident.Name] = true
				}
			}
		}
		return true
	})
}

var execMethods = map[string]bool{"Run": true, "Start": true, "Output": true, "CombinedOutput": true}

type envAssign struct {
	pos      token.Pos
	restores bool
	block    *ast.BlockStmt
}

// A go exec whose result is never bound to a variable is keyed under the empty
// name, which no Env assignment can satisfy — package-level declarations walk
// through here too, so a go exec in a var initializer always violates. An Env
// assignment counts only when it restores HOME itself; assigning nil, an empty
// slice, or an append that drops HOME is as much a violation as no assignment.
// The assignment that decides is the LAST one in source order before the
// command's first Run/Start/Output/CombinedOutput call, matching os/exec's
// last-wins duplicate-key resolution — and it must sit in a block enclosing
// that call, so a restore made only on a conditional branch does not count.
// Assignments after the first execution are ignored: os/exec forbids reusing
// a Cmd, so they configure nothing.
func goExecCommands(decl ast.Node, imports map[string]string, goNames map[string]bool) (map[string]token.Pos, map[string]bool) {
	commands := map[string]token.Pos{}
	claimed := map[*ast.CallExpr]bool{}
	assigns := map[string][]envAssign{}
	firstRun := map[string]token.Pos{}
	var stack []ast.Node
	ast.Inspect(decl, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		switch node := node.(type) {
		case *ast.AssignStmt:
			for i, target := range node.Lhs {
				sel, ok := target.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Env" || i >= len(node.Rhs) {
					continue
				}
				if name, ok := sel.X.(*ast.Ident); ok {
					assigns[name.Name] = append(assigns[name.Name], envAssign{
						pos:      node.Pos(),
						restores: restoresHome(node.Rhs[i], imports),
						block:    enclosingBlock(stack),
					})
				}
			}
			if len(node.Lhs) == 1 && len(node.Rhs) == 1 {
				if call, ok := node.Rhs[0].(*ast.CallExpr); ok && execsGoTool(call, imports, goNames) {
					if name, ok := node.Lhs[0].(*ast.Ident); ok {
						commands[name.Name] = call.Pos()
						claimed[call] = true
					}
				}
			}
		case *ast.CallExpr:
			if execsGoTool(node, imports, goNames) && !claimed[node] {
				if _, bound := commands[""]; !bound {
					commands[""] = node.Pos()
				}
			}
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && execMethods[sel.Sel.Name] {
				if name, ok := sel.X.(*ast.Ident); ok {
					if _, seen := firstRun[name.Name]; !seen {
						firstRun[name.Name] = node.Pos()
					}
				}
			}
		}
		stack = append(stack, node)
		return true
	})
	envBeforeRun := map[string]bool{}
	for name := range commands {
		envBeforeRun[name] = envSatisfied(assigns[name], firstRun[name])
	}
	return commands, envBeforeRun
}

func enclosingBlock(stack []ast.Node) *ast.BlockStmt {
	for i := len(stack) - 1; i >= 0; i-- {
		if block, ok := stack[i].(*ast.BlockStmt); ok {
			return block
		}
	}
	return nil
}

func envSatisfied(assigns []envAssign, firstRun token.Pos) bool {
	var last *envAssign
	for i := range assigns {
		candidate := &assigns[i]
		if firstRun.IsValid() && candidate.pos >= firstRun {
			continue
		}
		if last == nil || candidate.pos > last.pos {
			last = candidate
		}
	}
	switch {
	case last == nil || !last.restores:
		return false
	case !firstRun.IsValid():
		return true
	default:
		return last.block != nil && last.block.Pos() <= firstRun && firstRun <= last.block.End()
	}
}

// restoresHome accepts exactly the form the failure message prescribes: an
// append of os.Environ() whose LAST "HOME="-prefixed element is
// "HOME="+homeguard.RealHome() — os/exec resolves duplicate env keys
// last-wins, so a restore with a later "HOME=" element does not count.
func restoresHome(expr ast.Expr, imports map[string]string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || fun.Name != "append" || len(call.Args) < 2 {
		return false
	}
	base, ok := call.Args[0].(*ast.CallExpr)
	if !ok || !isPkgCall(base, imports, "os", "Environ") {
		return false
	}
	restored := false
	for _, arg := range call.Args[1:] {
		switch {
		case realHomeElement(arg, imports):
			restored = true
		case homeElement(arg):
			restored = false
		}
	}
	return restored
}

func realHomeElement(arg ast.Expr, imports map[string]string) bool {
	bin, ok := arg.(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD {
		return false
	}
	prefix, ok := literalString(bin.X)
	if !ok || prefix != "HOME=" {
		return false
	}
	home, ok := bin.Y.(*ast.CallExpr)
	return ok && isPkgCall(home, imports, homeguardPath, "RealHome")
}

func homeElement(arg ast.Expr) bool {
	if value, ok := literalString(arg); ok {
		return strings.HasPrefix(value, "HOME=")
	}
	bin, ok := arg.(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD {
		return false
	}
	prefix, ok := literalString(bin.X)
	return ok && strings.HasPrefix(prefix, "HOME=")
}

func execsGoTool(call *ast.CallExpr, imports map[string]string, goNames map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || imports[pkg.Name] != "os/exec" {
		return false
	}
	program := 0
	switch sel.Sel.Name {
	case "Command":
	case "CommandContext":
		program = 1
	default:
		return false
	}
	if len(call.Args) <= program {
		return false
	}
	if name, ok := literalString(call.Args[program]); ok {
		return name == "go"
	}
	name, ok := call.Args[program].(*ast.Ident)
	return ok && goNames[name.Name]
}

func isPkgCall(call *ast.CallExpr, imports map[string]string, path, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && imports[pkg.Name] == path
}

func importPaths(file *ast.File) map[string]string {
	paths := map[string]string{}
	for _, spec := range file.Imports {
		path, ok := literalString(spec.Path)
		if !ok {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		paths[name] = path
	}
	return paths
}

func isGoString(expr ast.Expr) bool {
	value, ok := literalString(expr)
	return ok && value == "go"
}

func literalString(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func parseFile(t *testing.T, fset *token.FileSet, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}
