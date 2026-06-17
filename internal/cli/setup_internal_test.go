package cli

import (
	"slices"
	"testing"
)

func TestPackAddArgs(t *testing.T) {
	tests := []struct {
		name string
		ver  string
		want []string
	}{
		{
			name: "dev tracks the default branch",
			ver:  "dev",
			want: []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes"},
		},
		{
			name: "empty tracks the default branch",
			ver:  "",
			want: []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes"},
		},
		{
			name: "stable tag pins the ref",
			ver:  "v1.2.3",
			want: []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes@v1.2.3"},
		},
		{
			name: "prerelease tag pins the ref",
			ver:  "v1.2.3-rc1",
			want: []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes@v1.2.3-rc1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := packAddArgs(tt.ver)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("packAddArgs(%q) = %v, want %v", tt.ver, got, tt.want)
			}
		})
	}
}
