package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/mod/semver"
)

func compare(target, current string) (int, error) {
	for _, value := range []string{target, current} {
		core := value
		if index := strings.IndexAny(core, "-+"); index >= 0 {
			core = core[:index]
		}
		if strings.Count(core, ".") != 2 || !semver.IsValid("v"+value) {
			return 0, fmt.Errorf("invalid release version %q", value)
		}
	}
	return semver.Compare("v"+target, "v"+current), nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: release-version TARGET CURRENT")
		os.Exit(1)
	}
	result, err := compare(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(result)
}
