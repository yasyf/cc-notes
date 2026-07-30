package fusefs

import (
	"fmt"
	"os"
	"testing"

	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/daemonkit/trust"
)

func TestMain(m *testing.M) {
	if handled, err := trust.RunVerifierChild(os.Args[1:], os.Stdout); handled {
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			homeguard.ChildExit(1)
		}
		homeguard.ChildExit(0)
	}
	homeguard.Main(m)
}
