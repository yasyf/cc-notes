package cli_test

import "testing"

func TestTaskDedupeFreshCLIMetadataCost(t *testing.T) {
	warmCounts := taskDedupeFreshCLIWork(t)
	for _, warm := range warmCounts {
		if warm["newfstatat"] > 512 || warm["fstat"] > 512 || warm["getdents64"] > 64 {
			t.Errorf("warm fresh CLI visited unrelated cache metadata: %v", warm)
		}
	}
	if len(warmCounts) == 2 {
		for _, name := range []string{"newfstatat", "fstat", "getdents64"} {
			if warmCounts[1][name] > warmCounts[0][name]+64 {
				t.Errorf("warm %s grew with unrelated tasks: %d at 32, %d at 10000", name, warmCounts[0][name], warmCounts[1][name])
			}
		}
	}
}
