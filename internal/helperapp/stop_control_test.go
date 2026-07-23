package helperapp

import (
	"testing"
)

func TestStopControlChildIgnoresUnrelatedModes(t *testing.T) {
	for _, arguments := range [][]string{nil, {"--other"}} {
		if recognized, err := RunStopControlChild(t.Context(), arguments); recognized || err != nil {
			t.Fatalf("RunStopControlChild(%q) = (%t, %v), want (false, nil)", arguments, recognized, err)
		}
	}
}
