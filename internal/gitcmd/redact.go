package gitcmd

import (
	"regexp"
	"strings"
)

var urlInText = regexp.MustCompile("[a-zA-Z][a-zA-Z0-9+.-]*://[^\\s\"'<>`]+")

// RedactURLs strips the userinfo, query and fragment from every URL in s,
// keeping scheme, host and path, because a presigned query or an embedded
// remote password is a bearer credential.
func RedactURLs(s string) string {
	return urlInText.ReplaceAllStringFunc(s, func(raw string) string {
		scheme, rest, _ := strings.Cut(raw, "://")
		if end := strings.IndexAny(rest, "?#"); end >= 0 {
			rest = rest[:end]
		}
		authority, path := rest, ""
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			authority, path = rest[:slash], rest[slash:]
		}
		return scheme + "://" + authority[strings.LastIndexByte(authority, '@')+1:] + path
	})
}
