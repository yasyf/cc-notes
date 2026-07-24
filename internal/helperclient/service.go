package helperclient

import "context"

var (
	serviceExecutablePath = ExecutablePath
	serviceRunProvision   = RunProvision
)

// ProvisionRepository invokes the fixed signed helper for one repository.
func ProvisionRepository(ctx context.Context, repoRoot string) error {
	executable, err := serviceExecutablePath()
	if err != nil {
		return err
	}
	return serviceRunProvision(ctx, executable, repoRoot)
}
