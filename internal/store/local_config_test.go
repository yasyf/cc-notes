package store

import "testing"

func TestIsLocalPushConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want bool
	}{
		{"empty", "", true},
		{"as written", "[cc-notes]\n\tsecluded = refs/cc-notes/notes/a\n[remote \"origin\"]\n\tpush = ^refs/cc-notes/notes/a\n", true},
		{"no trailing newline", "[cc-notes]\n\tsecluded = refs/cc-notes/notes/a", true},
		{"another section", "[user]\n\temail = other@example.test\n", false},
		{"another key", "[cc-notes]\n\temail = other@example.test\n", false},
		{"section smuggled into a remote header", "[remote \"x\"][user] email = other@example.test #\"]\n", false},
		{"subsection quote", "[remote \"x\" y\"]\n", false},
		{"include", "[include]\n\tpath = other\n", false},
		{"continuation", "[cc-notes]\n\tsecluded = a\\\n", false},
		{"comment", "[cc-notes]\n\tsecluded = a # b\n", false},
		{"value after a space", "[cc-notes]\n\tsecluded = a b\n", false},
		{"blank line", "[cc-notes]\n\n", false},
		{"positive push", "[remote \"x\"]\n\tpush = refs/heads/*\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLocalPushConfig([]byte(tc.data)); got != tc.want {
				t.Fatalf("IsLocalPushConfig(%q) = %t, want %t", tc.data, got, tc.want)
			}
		})
	}
}
