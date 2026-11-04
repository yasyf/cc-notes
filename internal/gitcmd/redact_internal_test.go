package gitcmd

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactURLs(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "presigned query in a Go transport error",
			in:   `get abc: Get "https://bucket.s3.amazonaws.com/objects/abc?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAEXAMPLE&X-Amz-Signature=feedface": EOF`,
			want: `get abc: Get "https://bucket.s3.amazonaws.com/objects/abc": EOF`,
		},
		{
			name: "remote credentials in git stderr",
			in:   "fatal: unable to access 'https://user:hunter2@github.com/o/r.git/': 403",
			want: "fatal: unable to access 'https://github.com/o/r.git/': 403",
		},
		{
			name: "every URL in the text",
			in:   "a http://h1/p?t=1 b ssh://git@h2:22/p#t c",
			want: "a http://h1/p b ssh://h2:22/p c",
		},
		{
			name: "userinfo with no path",
			in:   "https://token@github.com?x=1",
			want: "https://github.com",
		},
		{
			name: "text with no URL",
			in:   "fatal: couldn't find remote ref refs/x?y",
			want: "fatal: couldn't find remote ref refs/x?y",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactURLs(tc.in); got != tc.want {
				t.Fatalf("RedactURLs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCommandErrorRedactsForwardedStderrAndKeepsItForClassification(t *testing.T) {
	stderr := "fatal: unable to access 'https://user:hunter2@github.com/o/r.git/?X-Amz-Signature=feedface': 403"
	err := &commandError{args: []string{"fetch", "https://user:hunter2@github.com/o/r.git"}, stderr: stderr, err: errors.New("exit status 128")}
	got := err.Error()
	for _, secret := range []string{"hunter2", "X-Amz", "feedface"} {
		if strings.Contains(got, secret) {
			t.Fatalf("error %q leaks %q", got, secret)
		}
	}
	if !strings.Contains(got, "https://github.com/o/r.git/") {
		t.Fatalf("error %q lost the host and path", got)
	}
	if err.stderr != stderr {
		t.Fatalf("stderr = %q, classification needs the raw text", err.stderr)
	}
}
