package notes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/cc-notes/model"
)

var relevantRacyWindow = 2 * time.Second

const (
	relevantSymrefDepth   = 5
	relevantLinkHops      = 40
	relevantMissing       = "missing"
	defaultReplaceRefBase = "refs/replace/"
)

var relevantEnvKeys = []string{"HOME", "XDG_CONFIG_HOME", "EMAIL", noteStaleAfterEnv}

type commitResolver = func(context.Context, string) (model.SHA, error)

type relevantDeps struct {
	Branches []string `json:"branches,omitempty"`
	Commits  []string `json:"commits,omitempty"`
	Config   []string `json:"config,omitempty"`
}

type relevantInputs struct {
	client    *Client
	path      string
	filter    RelevantFilter
	variant   string
	gitDir    string
	commonDir string
	start     time.Time

	stamps     []fileStamp
	guards     []fileStamp
	watched    map[string]fileStamp
	guarded    map[string]fileStamp
	roots      []string
	depSeen    map[string]int
	lines      []string
	revalidate bool
	untrusted  bool
	noCache    bool

	exe        string
	git        string
	env        string
	symrefs    map[string]string
	head       model.SHA
	branch     model.Branch
	me         string
	baseRef    model.Branch
	base       model.SHA
	staleAfter time.Duration
	vars       map[string]string
	config     []gitcmd.ConfigEntry
	tips       map[string]model.SHA
	deps       relevantDeps
	fixed      []string
	values     map[string]model.SHA
}

func (c *Client) relevantInputs(ctx context.Context, p string, filter RelevantFilter, variant string, deps relevantDeps) (*relevantInputs, error) {
	in := &relevantInputs{
		client:    c,
		path:      p,
		filter:    filter,
		variant:   variant,
		gitDir:    c.s.GitDir(),
		commonDir: c.s.CommonDir(),
		start:     time.Now(),
		watched:   make(map[string]fileStamp),
		guarded:   make(map[string]fileStamp),
		depSeen:   make(map[string]int),
		symrefs:   make(map[string]string),
		vars:      make(map[string]string),
		tips:      make(map[string]model.SHA),
		deps:      relevantDeps{Config: deps.Config},
		values:    make(map[string]model.SHA),
	}
	in.roots = in.rootDirs()
	if err := in.watchExecutables(); err != nil {
		return nil, err
	}
	in.env = relevantEnv()
	in.record("env %s", in.env)
	if err := in.watchHead(ctx); err != nil {
		return nil, err
	}
	if err := in.watchConfig(ctx); err != nil {
		return nil, err
	}
	if err := in.watchHistory(); err != nil {
		return nil, err
	}
	if err := in.watchBranch(ctx); err != nil {
		return nil, err
	}
	if err := in.watchBase(ctx); err != nil {
		return nil, err
	}
	if err := in.watchEntities(ctx); err != nil {
		return nil, err
	}
	if err := in.setDeps(ctx, deps.Branches, deps.Commits); err != nil {
		return nil, err
	}
	in.base = in.values[string(in.baseRef)+"^{commit}"]
	in.record("base %s %s", in.baseRef, in.base)
	return in, nil
}

func (in *relevantInputs) record(format string, args ...any) {
	in.lines = append(in.lines, fmt.Sprintf(format, args...))
}

func (in *relevantInputs) watch(path string) fileStamp {
	return in.keep(stampOf(path))
}

func (in *relevantInputs) watchLink(path string) fileStamp {
	return in.keep(lstampOf(path))
}

func (in *relevantInputs) keep(s fileStamp) fileStamp {
	if prior, ok := in.watched[s.Path]; ok {
		return prior
	}
	in.watched[s.Path] = s
	in.stamps = append(in.stamps, s)
	in.resolve(s)
	return s
}

func (in *relevantInputs) resolve(s fileStamp) (string, bool) {
	// Walked as the kernel opens it: ".." after a symlink climbs out of its
	// target. The stamp, not the walk, decides the guard: a file that
	// appeared since the stamp can vanish again before close.
	path := s.Path
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			in.revalidate = true
			return "", false
		}
		path = joinPath(cwd, path)
	}
	root, rest := splitAbs(path)
	cur := root
	seen := make(map[string]fileStamp)
	linked, hops := 0, 0
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		fromLink := linked > 0
		if fromLink {
			linked--
		}
		switch name {
		case "", ".":
			continue
		case "..":
			if cur != root {
				cur = parentPath(cur)
			}
			continue
		}
		next := joinPath(cur, name)
		info, err := os.Lstat(next)
		if err != nil {
			if fromLink || slices.Contains(rest, "..") {
				in.revalidate = true
			} else {
				in.guard(cur, seen[cur])
			}
			if !s.Missing {
				in.untrusted = true
			}
			return "", false
		}
		if info.Mode()&os.ModeSymlink == 0 || (len(rest) == 0 && s.Link) {
			cur = next
			seen[cur] = stampFrom(cur, info, nil, false)
			if info.IsDir() && in.underRoot(cur) {
				in.guard(cur, seen[cur])
			}
			continue
		}
		in.guard(cur, seen[cur])
		hops++
		if hops > relevantLinkHops {
			in.revalidate = true
			return "", false
		}
		target, err := os.Readlink(next)
		if err != nil {
			in.untrusted = true
			return "", false
		}
		var hop []string
		if filepath.IsAbs(target) {
			root, hop = splitAbs(target)
			cur = root
		} else {
			hop = strings.Split(target, string(filepath.Separator))
		}
		rest = append(hop, rest...)
		linked += len(hop)
	}
	if s.Missing {
		parent := parentPath(cur)
		in.guard(parent, seen[parent])
		in.untrusted = true
	}
	return cur, true
}

func (in *relevantInputs) rootDirs() []string {
	var roots []string
	for _, dir := range []string{in.commonDir, in.gitDir} {
		if resolved, ok := in.resolve(fileStamp{Path: dir}); ok && !slices.Contains(roots, resolved) {
			roots = append(roots, resolved)
		}
	}
	return roots
}

func (in *relevantInputs) underRoot(dir string) bool {
	return slices.ContainsFunc(in.roots, func(root string) bool {
		return dir == root || strings.HasPrefix(dir, joinPath(root, ""))
	})
}

func splitAbs(path string) (root string, rest []string) {
	root = filepath.VolumeName(path) + string(filepath.Separator)
	return root, strings.Split(path[len(root):], string(filepath.Separator))
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		return dir + name
	}
	return dir + string(filepath.Separator) + name
}

func parentPath(path string) string {
	i := strings.LastIndexByte(path, filepath.Separator)
	if i <= 0 {
		return path[:i+1]
	}
	return path[:i]
}

func (in *relevantInputs) guard(dir string, seen fileStamp) {
	g, ok := in.guarded[dir]
	if !ok {
		g = stampOf(dir)
		in.guarded[dir] = g
		in.guards = append(in.guards, g)
	}
	if g.Missing || (seen.Path != "" && g != seen) {
		in.untrusted = true
	}
}

func nearestExisting(dir string) string {
	for {
		if _, err := os.Lstat(dir); err == nil {
			return dir
		}
		up := parentPath(dir)
		if up == dir {
			return dir
		}
		dir = up
	}
}

func (in *relevantInputs) watchBytes(label, path string) {
	if in.watch(path).Missing {
		in.record("%s %s %s", label, path, relevantMissing)
		return
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: a git-directory file this repository's git configuration names.
	if err != nil {
		in.untrusted = true
		return
	}
	in.record("%s %s %q", label, path, data)
}

func (in *relevantInputs) watchExecutables() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	exeStamp := in.watch(exe)
	revision := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				revision = setting.Value
			}
		}
	}
	in.record("exe %s %s %s %d %d %d %d", exe, version.Version, revision, exeStamp.Size, exeStamp.ModTime, exeStamp.Ctime, exeStamp.Inode)
	git, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("locate git: %w", err)
	}
	gitStamp := in.watch(git)
	in.record("git %s %d %d %d %d", git, gitStamp.Size, gitStamp.ModTime, gitStamp.Ctime, gitStamp.Inode)
	in.exe, in.git = exe, git
	return nil
}

func relevantEnv() string {
	env := os.Environ()
	slices.Sort(env)
	var digest strings.Builder
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") || slices.Contains(relevantEnvKeys, name) {
			digest.WriteString(kv)
			digest.WriteByte('\n')
		}
	}
	sum := sha256.Sum256([]byte(digest.String()))
	return hex.EncodeToString(sum[:])
}

func (in *relevantInputs) watchHead(ctx context.Context) error {
	in.watchRef("HEAD", 0)
	head, err := in.client.head(ctx)
	if err != nil {
		return err
	}
	in.head = head
	in.record("head %s", head)
	return nil
}

func (in *relevantInputs) watchRef(name string, depth int) {
	in.watch(joinPath(in.commonDir, "packed-refs"))
	file, ok := in.refFile(name)
	if !ok {
		in.revalidate = true
		return
	}
	s := in.watchLink(file)
	if s.Missing {
		return
	}
	target, ok := in.symref(file, s.Mode)
	if !ok {
		return
	}
	in.symrefs[name] = target
	in.record("symref %s %s", name, target)
	if in.followSymref(depth) {
		in.watchRef(target, depth+1)
	}
}

func (in *relevantInputs) followSymref(depth int) bool {
	if depth < relevantSymrefDepth {
		return true
	}
	in.revalidate = true
	return false
}

func (in *relevantInputs) symref(file string, mode os.FileMode) (string, bool) {
	if mode&os.ModeSymlink != 0 {
		return in.linkSymref(file)
	}
	data, err := os.ReadFile(file) //nolint:gosec // G304: a ref file inside this repository's git directories.
	if err != nil {
		in.untrusted = true
		return "", false
	}
	return symrefTarget(data)
}

func (in *relevantInputs) linkSymref(file string) (string, bool) {
	target, err := os.Readlink(file)
	if err != nil {
		in.untrusted = true
		return "", false
	}
	// git's files backend treats a link naming a ref under refs/ as a symref
	// and reads any other link through the filesystem, a path no stamp covers.
	if !strings.HasPrefix(target, "refs/") || plumbing.ReferenceName(target).Validate() != nil {
		in.revalidate = true
		return "", false
	}
	return target, true
}

func symrefTarget(data []byte) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "ref:")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func (in *relevantInputs) refFile(name string) (string, bool) {
	switch {
	case name == "HEAD" || rootPseudoref(name):
		return joinPath(in.gitDir, name), true
	case strings.HasPrefix(name, "refs/bisect/"), strings.HasPrefix(name, "refs/worktree/"), strings.HasPrefix(name, "refs/rewritten/"):
		return joinPath(in.gitDir, filepath.FromSlash(name)), true
	case strings.HasPrefix(name, "main-worktree/"):
		return joinPath(in.commonDir, filepath.FromSlash(strings.TrimPrefix(name, "main-worktree/"))), true
	case strings.HasPrefix(name, "worktrees/"), strings.HasPrefix(name, "refs/"):
		return joinPath(in.commonDir, filepath.FromSlash(name)), true
	}
	return "", false
}

func rootPseudoref(name string) bool {
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for _, r := range name {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func (in *relevantInputs) watchDirs(dir string) {
	s := in.watch(dir)
	if s.Missing || !s.Mode.IsDir() {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		in.untrusted = true
		return
	}
	for _, e := range entries {
		path := joinPath(dir, e.Name())
		switch {
		case e.IsDir():
			in.watchDirs(path)
			continue
		case strings.HasSuffix(e.Name(), ".lock"):
			continue
		}
		if target, ok := in.symref(path, e.Type()); ok {
			in.watchDep(target, 1)
		}
	}
}

func cleanRevName(rev string) bool {
	for i := 0; i < len(rev); i++ {
		if rev[i] < 0x20 || rev[i] == 0x7f {
			return false
		}
	}
	return rev != ""
}

func (in *relevantInputs) watchDep(name string, depth int) {
	if !cleanRevName(name) {
		return
	}
	if seen, ok := in.depSeen[name]; ok && seen <= depth {
		return
	}
	in.depSeen[name] = depth
	switch {
	case name == "HEAD":
		in.watchRef(name, depth)
		return
	case rootPseudoref(name):
		// An anchored root ref is one leaf stamp; one reached as a symref
		// target would add a leaf per alias, and the git dir itself is too
		// noisy to stamp as a directory.
		if depth > 0 {
			in.revalidate = true
			return
		}
		in.watchRef(name, depth)
		return
	}
	file, ok := in.refFile(name)
	if !ok {
		in.revalidate = true
		return
	}
	in.watch(nearestExisting(parentPath(file)))
	in.watch(joinPath(in.commonDir, "packed-refs"))
	info, err := os.Lstat(file)
	if err != nil || info.IsDir() {
		return
	}
	if target, ok := in.symref(file, info.Mode()); ok && in.followSymref(depth) {
		in.watchDep(target, depth+1)
	}
}

func (in *relevantInputs) watchRev(rev string) {
	switch {
	case plumbing.IsHash(rev), !cleanRevName(rev):
		return
	case strings.HasPrefix(rev, "main-worktree/"), strings.HasPrefix(rev, "worktrees/"):
		in.watchRef(rev, 0)
		return
	case strings.ContainsAny(rev, "~^:@{} \t") || strings.Contains(rev, ".."), hexAbbreviation(rev):
		in.revalidate = true
		return
	}
	for _, candidate := range dwimCandidates(rev) {
		in.watchDep(candidate, 0)
	}
}

func hexAbbreviation(rev string) bool {
	if len(rev) < 4 || len(rev) >= 40 {
		return false
	}
	for _, r := range rev {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func dwimCandidates(name string) []string {
	if strings.HasPrefix(name, "refs/") {
		return []string{name}
	}
	var candidates []string
	if rootPseudoref(name) {
		candidates = append(candidates, name)
	}
	return append(candidates, "refs/"+name, "refs/tags/"+name, "refs/heads/"+name, "refs/remotes/"+name, "refs/remotes/"+name+"/HEAD")
}

func (in *relevantInputs) watchConfig(ctx context.Context) error {
	git := in.client.s.Git
	candidates := in.deps.Config
	if len(candidates) == 0 {
		entries, err := git.ConfigOrigins(ctx)
		if err != nil {
			return err
		}
		varList, err := git.VarList(ctx)
		if err != nil {
			return err
		}
		if candidates, _, err = in.configFiles(ctx, entries, varList); err != nil {
			return err
		}
	}
	for _, file := range candidates {
		in.watch(file)
	}
	entries, err := git.ConfigOrigins(ctx)
	if err != nil {
		return err
	}
	varList, err := git.VarList(ctx)
	if err != nil {
		return err
	}
	in.config = entries
	for _, e := range entries {
		in.record("config %s %s %q %t", e.Origin, e.Key, e.Value, e.HasValue)
	}
	vars, dump := splitVarList(varList)
	for _, line := range vars {
		name, value, _ := strings.Cut(line, "=")
		if name == "GIT_AUTHOR_IDENT" || name == "GIT_COMMITTER_IDENT" {
			value = value[:strings.LastIndexByte(value, '>')+1]
		}
		in.record("var %s=%q", name, value)
		if _, seen := in.vars[name]; !seen {
			in.vars[name] = value
		}
	}
	files, origins, err := in.configFiles(ctx, entries, varList)
	if err != nil {
		return err
	}
	in.auditConfig(entries, files, origins, dump)
	in.deps.Config = slices.Sorted(maps.Keys(in.configStamped()))
	if _, in.me, err = git.AuthorIdent(ctx); err != nil {
		return err
	}
	in.record("me %s", in.me)
	if !in.explicitEmail() {
		in.revalidate = true
	}
	staleAfter, err := in.client.NoteStaleAfter(ctx)
	if err != nil {
		return err
	}
	in.staleAfter = staleAfter
	in.record("stale %s", staleAfter)
	return nil
}

func (in *relevantInputs) configStamped() map[string]bool {
	stamped := make(map[string]bool)
	for _, file := range in.deps.Config {
		stamped[file] = true
	}
	return stamped
}

func (in *relevantInputs) configFiles(ctx context.Context, entries []gitcmd.ConfigEntry, varList string) (files []string, origins map[string]bool, err error) {
	origins = make(map[string]bool)
	add := func(path string) {
		if !slices.Contains(files, path) {
			files = append(files, path)
		}
	}
	add(joinPath(in.commonDir, "config"))
	vars, _ := splitVarList(varList)
	for _, line := range vars {
		name, value, _ := strings.Cut(line, "=")
		if name != "GIT_CONFIG_SYSTEM" && name != "GIT_CONFIG_GLOBAL" {
			continue
		}
		if !filepath.IsAbs(value) {
			in.revalidate = true
			continue
		}
		add(value)
	}
	home := os.Getenv("HOME")
	worktreeConfig := false
	for _, e := range entries {
		if e.Key == "extensions.worktreeconfig" {
			worktreeConfig = configTrue(e)
		}
		origin, fromFile := strings.CutPrefix(e.Origin, "file:")
		if fromFile {
			if !filepath.IsAbs(origin) {
				base := in.gitDir
				if !in.client.s.Bare() {
					if base, err = in.client.s.Root(ctx); err != nil {
						return nil, nil, fmt.Errorf("config origin %s: %w", origin, err)
					}
				}
				origin = joinPath(base, origin)
			}
			origins[origin] = true
			add(origin)
		}
		if !includeKey(e.Key) {
			continue
		}
		dir := ""
		if fromFile {
			dir = parentPath(origin)
		}
		if target, ok := in.includeTarget(e.Value, dir, home); ok {
			add(target)
		}
	}
	if worktreeConfig {
		add(joinPath(in.gitDir, "config.worktree"))
	}
	return files, origins, nil
}

func configTrue(e gitcmd.ConfigEntry) bool {
	if !e.HasValue {
		return true
	}
	switch strings.ToLower(e.Value) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off", "":
		return false
	}
	n, err := strconv.ParseInt(e.Value, 10, 64)
	return err == nil && n != 0
}

func includeKey(key string) bool {
	return key == "include.path" || (strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path"))
}

func (in *relevantInputs) includeTarget(value, dir, home string) (string, bool) {
	switch {
	case strings.HasPrefix(value, "~/"):
		return joinPath(home, value[2:]), true
	case strings.HasPrefix(value, "~"), strings.HasPrefix(value, "%(prefix)"):
		in.revalidate = true
		return "", false
	case filepath.IsAbs(value):
		return value, true
	case dir == "":
		in.revalidate = true
		return "", false
	}
	return joinPath(dir, value), true
}

func (in *relevantInputs) auditConfig(entries []gitcmd.ConfigEntry, files []string, origins map[string]bool, dump string) {
	for _, file := range files {
		s, stamped := in.watched[file]
		if !stamped {
			in.untrusted = true
			s = in.watch(file)
		}
		if origins[file] && s.Missing {
			in.untrusted = true
		}
		in.deps.Config = append(in.deps.Config, file)
	}
	if dump != configText(entries) {
		in.untrusted = true
	}
}

func configText(entries []gitcmd.ConfigEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Key)
		if e.HasValue {
			b.WriteByte('=')
			b.WriteString(e.Value)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func splitVarList(varList string) (vars []string, dump string) {
	lines := strings.Split(strings.TrimSuffix(varList, "\n"), "\n")
	first := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "GIT_COMMITTER_IDENT=") {
			first = i
			break
		}
	}
	if first > 0 {
		dump = strings.Join(lines[:first], "\n") + "\n"
	}
	for _, line := range lines[first:] {
		if varLine(line) || len(vars) == 0 {
			vars = append(vars, line)
			continue
		}
		vars[len(vars)-1] += "\n" + line
	}
	return vars, dump
}

func varLine(line string) bool {
	name, _, ok := strings.Cut(line, "=")
	if !ok || !strings.HasPrefix(name, "GIT_") {
		return false
	}
	for _, r := range name {
		if (r < 'A' || r > 'Z') && r != '_' {
			return false
		}
	}
	return true
}

func (in *relevantInputs) explicitEmail() bool {
	if os.Getenv("GIT_AUTHOR_EMAIL") != "" || os.Getenv("EMAIL") != "" {
		return true
	}
	return in.configured("user.email") || in.configured("author.email")
}

func (in *relevantInputs) configured(key string) bool {
	return slices.ContainsFunc(in.config, func(e gitcmd.ConfigEntry) bool { return e.Key == key })
}

func (in *relevantInputs) watchHistory() error {
	in.watch(joinPath(in.commonDir, "shallow"))
	grafted, err := in.client.s.Repo.RefreshShallow()
	if err != nil {
		return err
	}
	for _, sha := range grafted {
		in.record("shallow %s", sha)
	}
	if file := os.Getenv("GIT_SHALLOW_FILE"); file != "" {
		in.watchBytes("shallow-file", file)
	}
	grafts := os.Getenv("GIT_GRAFT_FILE")
	if grafts == "" {
		grafts = joinPath(joinPath(in.commonDir, "info"), "grafts")
	}
	in.watchBytes("grafts", grafts)
	in.watchDirs(joinPath(in.commonDir, filepath.FromSlash(strings.TrimSuffix(replaceRefBase(), "/"))))
	return nil
}

func replaceRefBase() string {
	base := os.Getenv("GIT_REPLACE_REF_BASE")
	if base == "" {
		return defaultReplaceRefBase
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base
}

func (in *relevantInputs) detached() bool {
	return in.filter.Branch == "" && in.symrefs["HEAD"] == ""
}

func (in *relevantInputs) watchBranch(ctx context.Context) error {
	if in.detached() {
		in.watchDirs(joinPath(in.commonDir, filepath.FromSlash("refs/heads")))
		in.watchRef("refs/remotes/origin/HEAD", 0)
	}
	branch, err := in.client.resolveRelevantBranch(ctx, in.filter.Branch)
	if err != nil {
		return err
	}
	in.branch = branch
	in.record("branch %s", branch)
	return nil
}

func (in *relevantInputs) watchBase(ctx context.Context) error {
	if in.filter.Base == "" {
		in.watchRef("refs/remotes/origin/HEAD", 0)
		if short, ok := strings.CutPrefix(in.symrefs["refs/remotes/origin/HEAD"], "refs/remotes/"); ok {
			for _, candidate := range []string{"refs/" + short, "refs/tags/" + short, "refs/heads/" + short} {
				in.watchDep(candidate, 0)
				in.fixed = append(in.fixed, candidate)
			}
		}
	}
	baseRef, err := in.client.resolveRelevantBase(ctx, in.filter.Base)
	if err != nil {
		return err
	}
	in.baseRef = baseRef
	in.watchRev(string(baseRef))
	in.fixed = append(in.fixed, string(baseRef)+"^{commit}")
	return nil
}

func (in *relevantInputs) watchEntities(ctx context.Context) error {
	for _, root := range relevantRefRoots {
		in.watch(joinPath(in.commonDir, filepath.FromSlash(strings.TrimSuffix(root, "/"))))
	}
	entries, err := in.client.s.Git.RefEntries(ctx, append(slices.Clone(relevantRefRoots), replaceRefBase())...)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Symref != "" {
			in.record("tip sym %s %s %s", e.Ref, e.Tip, e.Symref)
			continue
		}
		in.record("tip hash %s %s", e.Ref, e.Tip)
		for _, root := range relevantRefRoots {
			if refs.DirectChild(root, e.Ref) {
				in.tips[e.Ref] = e.Tip
				break
			}
		}
	}
	return nil
}

func (in *relevantInputs) setDeps(ctx context.Context, branches, commits []string) error {
	in.deps.Branches = slices.Compact(slices.Sorted(slices.Values(branches)))
	in.deps.Commits = slices.Compact(slices.Sorted(slices.Values(commits)))
	for _, ref := range in.deps.Branches {
		in.watchDep(ref, 0)
	}
	for _, rev := range in.deps.Commits {
		in.watchRev(rev)
	}
	var pending []string
	for _, expr := range in.depExprs() {
		if _, ok := in.values[expr]; ok {
			continue
		}
		if !cleanRevName(expr) {
			in.values[expr] = ""
			continue
		}
		pending = append(pending, expr)
	}
	if len(pending) == 0 {
		return nil
	}
	resolved, err := in.client.s.Git.ResolveRevs(ctx, pending)
	if err != nil {
		return err
	}
	maps.Copy(in.values, resolved)
	return nil
}

func (in *relevantInputs) depExprs() []string {
	exprs := slices.Clone(in.fixed)
	for _, ref := range in.deps.Branches {
		exprs = append(exprs, ref+"^{commit}")
	}
	for _, rev := range in.deps.Commits {
		exprs = append(exprs, rev+"^{commit}")
	}
	slices.Sort(exprs)
	return slices.Compact(exprs)
}

func (in *relevantInputs) resolveCommit(_ context.Context, rev string) (model.SHA, error) {
	if plumbing.IsHash(rev) {
		return model.SHA(rev), nil
	}
	sha, ok := in.values[rev+"^{commit}"]
	if !ok {
		return "", fmt.Errorf("resolve %s: revision was not captured", rev)
	}
	if sha == "" {
		return "", fmt.Errorf("resolve %s: %w", rev, gitcmd.ErrRevNotFound)
	}
	return sha, nil
}

func (in *relevantInputs) auditWorktree(ctx context.Context, anchors []string) error {
	if os.Getenv("GIT_ATTR_SOURCE") != "" || in.configured("attr.tree") {
		in.noCache = true
		return nil
	}
	if len(anchors) == 0 {
		return nil
	}
	drivers, err := in.client.s.Git.CheckAttr(ctx, "filter", anchors)
	if err != nil {
		return err
	}
	for _, driver := range drivers {
		if in.configured("filter."+driver+".clean") || in.configured("filter."+driver+".process") {
			in.noCache = true
		}
	}
	return nil
}

func (in *relevantInputs) key() string {
	var digest strings.Builder
	digest.WriteString(relevantCacheName(in.gitDir, in.client.s.Git.Dir, in.path, in.filter, in.variant))
	for _, line := range in.lines {
		digest.WriteByte('\n')
		digest.WriteString(line)
	}
	for _, expr := range in.depExprs() {
		value := string(in.values[expr])
		if value == "" {
			value = relevantMissing
		}
		digest.WriteString("\ndep " + expr + " " + value)
	}
	sum := sha256.Sum256([]byte(digest.String()))
	return hex.EncodeToString(sum[:])
}

func (in *relevantInputs) close() bool {
	for _, s := range in.stamps {
		if restamp(s) != s {
			return false
		}
	}
	for _, g := range in.guards {
		if restamp(g) != g {
			return false
		}
	}
	return true
}

func (in *relevantInputs) racy() bool {
	racyAt := func(s fileStamp) bool { return s.racy(in.start) }
	return in.untrusted || slices.ContainsFunc(in.stamps, racyAt) || slices.ContainsFunc(in.guards, racyAt)
}

func (in *relevantInputs) fill(e *relevantCacheEntry) {
	e.Built = in.start.UnixNano()
	e.Env = in.env
	e.Exe = in.exe
	e.Git = in.git
	e.Racy = in.racy()
	e.Revalidate = in.revalidate
	e.Stamps = in.stamps
	e.Deps = in.deps
}
