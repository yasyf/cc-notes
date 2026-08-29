package gitobj

import "testing"

func TestUnderRefs(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"refs", true},
		{"refs/cc-notes/notes", true},
		{"refs/remotes/prhead", true},
		{"./refs/heads", true},
		{"objects/pack", false},
		{"logs/refs/heads", false},
		{"refsx", false},
		{".", false},
	}
	for _, tc := range cases {
		if got := underRefs(tc.path); got != tc.want {
			t.Errorf("underRefs(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
