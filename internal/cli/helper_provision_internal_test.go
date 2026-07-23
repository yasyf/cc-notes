package cli

import "context"

func init() {
	provisionRepository = func(context.Context, string) error { return nil }
}
