package helperapp

import (
	"testing"

	"github.com/yasyf/daemonkit/daemon"
	"github.com/yasyf/daemonkit/wire"
)

func TestServiceHealthRequiresExactHealthyRuntime(t *testing.T) {
	const build = "cc-notes-v1"
	for _, test := range []struct {
		name    string
		health  daemon.Health
		ready   bool
		wantErr bool
	}{
		{name: "healthy", health: daemon.Health{Build: build, Protocol: int(wire.ProtocolVersion), State: daemon.StateHealthy}, ready: true},
		{name: "bootstrapping", health: daemon.Health{Build: build, Protocol: int(wire.ProtocolVersion), State: daemon.StateDegraded}},
		{name: "draining", health: daemon.Health{Build: build, Protocol: int(wire.ProtocolVersion), State: daemon.StateHealthy, Draining: true}},
		{name: "failed", health: daemon.Health{Build: build, Protocol: int(wire.ProtocolVersion), State: daemon.StateFailed}, wantErr: true},
		{name: "wrong build", health: daemon.Health{Build: "other", Protocol: int(wire.ProtocolVersion), State: daemon.StateHealthy}, wantErr: true},
		{name: "wrong protocol", health: daemon.Health{Build: build, Protocol: int(wire.ProtocolVersion) + 1, State: daemon.StateHealthy}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ready, err := serviceHealthReady(build, test.health)
			if (err != nil) != test.wantErr || ready != test.ready {
				t.Fatalf("serviceHealthReady = (%v, %v), want (%v, error=%v)", ready, err, test.ready, test.wantErr)
			}
		})
	}
}
