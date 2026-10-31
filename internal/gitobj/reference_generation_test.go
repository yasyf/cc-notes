package gitobj

import (
	"testing"
	"time"
)

func TestReferenceGenerationNativeSettlement(t *testing.T) {
	taken := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name    string
		stamp   referenceStamp
		settled bool
	}{
		{"supported recent", referenceStamp{mtime: taken.UnixNano(), nativeKnown: true, nativeStable: true, writeCounter: 7}, true},
		{"changing recent", referenceStamp{mtime: taken.UnixNano(), nativeKnown: true, writeCounter: 8}, false},
		{"changing old", referenceStamp{mtime: taken.Add(-time.Hour).UnixNano(), nativeKnown: true, writeCounter: 8}, false},
		{"unsupported recent", referenceStamp{mtime: taken.UnixNano()}, false},
		{"unsupported old", referenceStamp{mtime: taken.Add(-time.Hour).UnixNano()}, true},
		{"absent", referenceStamp{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, generation := range []ReferenceGeneration{{packed: tc.stamp}, {loose: tc.stamp}} {
				if got := generation.SettledAt(taken); got != tc.settled {
					t.Fatalf("SettledAt = %t, want %t for %+v", got, tc.settled, generation)
				}
			}
		})
	}
}

func TestReferenceGenerationNativeKey(t *testing.T) {
	base := ReferenceGeneration{directory: "refs/cc-notes/docs", packed: referenceStamp{device: 1, inode: 2, mtime: 3, ctime: 4, size: 5, nativeKnown: true, nativeStable: true, writeCounter: 6}}
	for _, change := range []func(*referenceStamp){
		func(s *referenceStamp) { s.device++ },
		func(s *referenceStamp) { s.inode++ },
		func(s *referenceStamp) { s.mtime++ },
		func(s *referenceStamp) { s.ctime++ },
		func(s *referenceStamp) { s.size++ },
		func(s *referenceStamp) { s.writeCounter++ },
		func(s *referenceStamp) { s.nativeKnown = false },
	} {
		changed := base
		change(&changed.packed)
		if changed.Key() == base.Key() {
			t.Fatalf("generation identity discarded changed stamp: %+v", changed)
		}
	}
}
