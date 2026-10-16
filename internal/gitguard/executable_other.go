//go:build !linux

package gitguard

import "os"

func executable() (string, error) { return os.Executable() }
