package helperapp

import (
	"testing"

	"github.com/yasyf/fusekit/mountproto"
)

func TestServiceHealthRequiresExactHealthyRuntime(t *testing.T) {
	const build = "cc-notes-v1"
	for _, test := range []struct {
		name    string
		health  mountproto.RuntimeHealthResponse
		ready   bool
		wantErr bool
	}{
		{name: "healthy", health: mountproto.RuntimeHealthResponse{
			RuntimeBuild: build, RuntimeProtocol: mountproto.RuntimeProtocolVersion,
			ProcessGeneration: "process-1", Ready: true, State: mountproto.RuntimeStateHealthy,
		}, ready: true},
		{name: "bootstrapping", health: mountproto.RuntimeHealthResponse{
			RuntimeBuild: build, RuntimeProtocol: mountproto.RuntimeProtocolVersion,
			ProcessGeneration: "process-1", State: mountproto.RuntimeStateDegraded,
		}},
		{name: "degraded but ready", health: mountproto.RuntimeHealthResponse{
			RuntimeBuild: build, RuntimeProtocol: mountproto.RuntimeProtocolVersion,
			ProcessGeneration: "process-1", Ready: true, State: mountproto.RuntimeStateDegraded,
		}},
		{name: "draining", health: mountproto.RuntimeHealthResponse{
			RuntimeBuild: build, RuntimeProtocol: mountproto.RuntimeProtocolVersion,
			ProcessGeneration: "process-1", State: mountproto.RuntimeStateDraining, Draining: true,
		}},
		{name: "missing generation", health: mountproto.RuntimeHealthResponse{
			RuntimeBuild: build, RuntimeProtocol: mountproto.RuntimeProtocolVersion,
			Ready: true, State: mountproto.RuntimeStateHealthy,
		}, wantErr: true},
		{name: "wrong build", health: mountproto.RuntimeHealthResponse{
			RuntimeBuild: "other", RuntimeProtocol: mountproto.RuntimeProtocolVersion,
			ProcessGeneration: "process-1", Ready: true, State: mountproto.RuntimeStateHealthy,
		}, wantErr: true},
		{name: "wrong protocol", health: mountproto.RuntimeHealthResponse{
			RuntimeBuild: build, RuntimeProtocol: mountproto.RuntimeProtocolVersion + 1,
			ProcessGeneration: "process-1", Ready: true, State: mountproto.RuntimeStateHealthy,
		}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ready, err := serviceHealthReady(build, test.health)
			if (err != nil) != test.wantErr || ready != test.ready {
				t.Fatalf("serviceHealthReady = (%v, %v), want (%v, error=%v)", ready, err, test.ready, test.wantErr)
			}
		})
	}
}
