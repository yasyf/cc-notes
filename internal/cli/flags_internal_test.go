package cli

import (
	"testing"

	"github.com/spf13/pflag"
)

// TestBindLimit pins the one canonical "0 = all" usage string and that the
// caller's tuned default is preserved.
func TestBindLimit(t *testing.T) {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	var limit int
	bindLimit(fs, &limit, 20)

	f := fs.Lookup("limit")
	if f == nil {
		t.Fatal("bindLimit did not register --limit")
	}
	if got, want := f.DefValue, "20"; got != want {
		t.Errorf("default = %q, want %q", got, want)
	}
	if got, want := f.Usage, "maximum results (0 = all)"; got != want {
		t.Errorf("usage = %q, want %q", got, want)
	}
}

// TestBindRemote pins the harmonized wording: init keeps its fixed-default "wire"
// phrasing, sync keeps the empty-default derive-from-wiring fallback note. These
// reproduce the two live --remote usages exactly so applying the binder later is
// surface-neutral.
func TestBindRemote(t *testing.T) {
	tests := []struct {
		name      string
		def       string
		verb      string
		wantDef   string
		wantUsage string
	}{
		{"init wire", "origin", "wire", "origin", "remote to wire"},
		{"sync derive", "", "sync with", "", "remote to sync with (default: every cc-notes-wired remote, else origin)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
			var remote string
			bindRemote(fs, &remote, tt.def, tt.verb)

			f := fs.Lookup("remote")
			if f == nil {
				t.Fatal("bindRemote did not register --remote")
			}
			if f.DefValue != tt.wantDef {
				t.Errorf("default = %q, want %q", f.DefValue, tt.wantDef)
			}
			if f.Usage != tt.wantUsage {
				t.Errorf("usage = %q, want %q", f.Usage, tt.wantUsage)
			}
		})
	}
}
