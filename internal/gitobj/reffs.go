package gitobj

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5"
)

// refsDir is the ref tree's root inside a git directory. go-git keeps its own
// spelling unexported.
const refsDir = "refs"

// unlockedFS withholds ref lock files from listings of the ref tree. git's own
// enumeration never treats a .lock entry as a ref; go-git's loose-ref walk
// reads every regular file and aborts the whole listing on the first empty
// one, and offers no skip hook.
type unlockedFS struct {
	billy.Filesystem
}

func (fs unlockedFS) ReadDir(path string) ([]os.FileInfo, error) {
	entries, err := fs.Filesystem.ReadDir(path)
	if err != nil || !underRefs(path) {
		return entries, err
	}
	kept := entries[:0]
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") {
			kept = append(kept, entry)
		}
	}
	return kept, nil
}

func underRefs(path string) bool {
	clean := filepath.Clean(path)
	return clean == refsDir || strings.HasPrefix(clean, refsDir+string(filepath.Separator))
}
