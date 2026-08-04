//go:build darwin

package helperdeployment

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/homeguard"
)

func TestMain(m *testing.M) { homeguard.Main(m) }
