package sync

import (
	"bytes"
	"context"
	"regexp"
	"testing"
	"time"
)

func TestPhaseWritesALineOnlyForAPhasePastTheBound(t *testing.T) {
	saved := slowPhaseAfter
	slowPhaseAfter = 50 * time.Millisecond
	t.Cleanup(func() { slowPhaseAfter = saved })
	var out bytes.Buffer
	ctx := WithSlowPhases(context.Background(), &out)

	Phase(ctx, "pushing 1 refs")()
	if out.Len() != 0 {
		t.Fatalf("a phase that ended at once wrote %q, want nothing", out.String())
	}
	end := Phase(ctx, "downloading attachments")
	time.Sleep(4 * slowPhaseAfter)
	end()
	want := regexp.MustCompile(`^ccn sync: still downloading attachments after 50ms\nccn sync: downloading attachments took \d+s\n$`)
	if !want.MatchString(out.String()) {
		t.Fatalf("a phase past the bound wrote %q, want the still line then the took line", out.String())
	}
}
