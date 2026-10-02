package gitcmd

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// routingEnvWant pins the exact deny-list. A name dropped from routingEnv lets
// an inherited variable reroute a records command into the thin repository; a
// name added here must be argued for, since the list is a deny-list and not a
// GIT_* wipe.
var routingEnvWant = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_INDEX_FILE",
	"GIT_SHALLOW_FILE",
	"GIT_GRAFT_FILE",
	"GIT_REPLACE_REF_BASE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_NAMESPACE",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
	"GIT_QUARANTINE_PATH",
	"GIT_REFERENCE_BACKEND",
	"GIT_CONFIG",
	"GIT_PREFIX",
	"GIT_IMPLICIT_WORK_TREE",
}

// keptEnv samples the variables a backend command must keep: config
// injection, credential and SSH plumbing, identity, and a GIT_DIR-prefixed
// name that an over-broad prefix match would wrongly drop.
var keptEnv = []string{
	"GIT_CONFIG_COUNT=1",
	"GIT_CONFIG_KEY_0=maintenance.auto",
	"GIT_CONFIG_VALUE_0=false",
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_CONFIG_PARAMETERS='http.extraheader=x'",
	"GIT_ASKPASS=/usr/bin/true",
	"GIT_SSH_COMMAND=ssh -i key",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_AUTHOR_NAME=Agent",
	"GIT_DIRECTORY=/not/a/routing/variable",
	"HOME=/home/agent",
	"PATH=/usr/bin",
}

func TestBackendEnvironDropsExactlyRoutingVars(t *testing.T) {
	if !slices.Equal(routingEnv, routingEnvWant) {
		t.Fatalf("routingEnv = %q, want %q", routingEnv, routingEnvWant)
	}
	for _, name := range routingEnvWant {
		t.Run(name, func(t *testing.T) {
			env := slices.Concat([]string{name + "=/elsewhere"}, keptEnv, []string{name + "="})
			got := Backend("/repo").environ(env)
			if !slices.Equal(got, keptEnv) {
				t.Fatalf("Backend environ dropped the wrong set:\n got %q\nwant %q", got, keptEnv)
			}
			if env[0] != name+"=/elsewhere" || env[len(env)-1] != name+"=" {
				t.Fatalf("environ mutated its input: %q", env)
			}
		})
	}
	t.Run("checkout handle inherits nil", func(t *testing.T) {
		if got := (Git{Dir: "/repo"}).environ(nil); got != nil {
			t.Fatalf("checkout environ(nil) = %q, want nil so exec inherits the process environment", got)
		}
	})
	t.Run("checkout handle passes routing through", func(t *testing.T) {
		env := []string{"GIT_DIR=/elsewhere", "PATH=/usr/bin"}
		got := (Git{Dir: "/repo"}).environ(env)
		if !slices.Equal(got, env) {
			t.Fatalf("checkout environ = %q, want %q unchanged", got, env)
		}
	})
	t.Run("backend nil env is the scrubbed process environment", func(t *testing.T) {
		t.Setenv("GIT_DIR", "/elsewhere")
		t.Setenv("GIT_ASKPASS", "/usr/bin/true")
		got := Backend("/repo").environ(nil)
		if len(got) != len(os.Environ())-1 {
			t.Fatalf("backend environ(nil) has %d entries, want the process environment's %d minus GIT_DIR", len(got), len(os.Environ()))
		}
		if !slices.Contains(got, "GIT_ASKPASS=/usr/bin/true") {
			t.Fatalf("backend environ(nil) dropped GIT_ASKPASS: %q", got)
		}
		if i := slices.IndexFunc(got, func(kv string) bool { return strings.HasPrefix(kv, "GIT_DIR=") }); i >= 0 {
			t.Fatalf("backend environ(nil) kept %q", got[i])
		}
	})
}
