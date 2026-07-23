package helperapp

import (
	"context"

	"github.com/yasyf/fusekit/holder"
)

// RunStopControlChild recognizes and runs the exact hidden runtime-settlement role.
func RunStopControlChild(ctx context.Context, arguments []string) (bool, error) {
	if len(arguments) == 0 || arguments[0] != holder.StopControlChildArguments()[0] {
		return false, nil
	}
	plan, err := NewRuntimePlan(ctx)
	if err != nil {
		return true, err
	}
	return holder.RunStopControlChild(ctx, arguments, holder.StopControlChildConfig{
		Socket: plan.Paths().Socket,
	})
}
