//go:build darwin

package helperapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helperdeployment"
	"github.com/yasyf/cc-notes/internal/version"
)

// PackagedApplication returns the bundle the running executable belongs to.
// The installer is the payload: package-install lands the very bundle it is
// executing from, so no separate bootstrap binary has to exist first.
func PackagedApplication() (string, error) {
	executable, err := helperclient.CanonicalExecutable()
	if err != nil {
		return "", err
	}
	macOS := filepath.Dir(executable)
	contents := filepath.Dir(macOS)
	appPath := filepath.Dir(contents)
	if filepath.Base(executable) != ExecutableName || filepath.Base(macOS) != "MacOS" ||
		filepath.Base(contents) != "Contents" || filepath.Base(appPath) != ExecutableName+".app" {
		return "", errors.New("cc-notes helper: executable is not the packaged CCNotesHelper app child")
	}
	return appPath, nil
}

// RunVerb dispatches one deployment verb, reporting whether it recognized it.
func RunVerb(ctx context.Context, arguments []string) (bool, error) {
	if len(arguments) == 0 {
		return false, nil
	}
	verb, rest := arguments[0], arguments[1:]
	if verb == helperclient.VerbVersion {
		if len(rest) != 0 {
			return true, fmt.Errorf("cc-notes helper: %s takes no arguments", verb)
		}
		_, err := fmt.Fprintln(os.Stdout, version.String())
		return true, err
	}
	nullary := map[string]func(context.Context) error{
		helperclient.VerbPackageInstall:   applyPackagedSelf,
		helperclient.VerbPackageUninstall: helperdeployment.UninstallPackage,
		helperclient.VerbServiceInstall:   helperdeployment.ActivateService,
		helperclient.VerbServiceUninstall: helperdeployment.DeactivateService,
	}
	if operation, known := nullary[verb]; known {
		if len(rest) != 0 {
			return true, fmt.Errorf("cc-notes helper: %s takes no arguments", verb)
		}
		return true, operation(ctx)
	}
	if verb == helperclient.VerbProvisionRepo {
		if len(rest) != 1 {
			return true, fmt.Errorf("cc-notes helper: %s takes exactly one repository path", verb)
		}
		return true, provisionRepository(ctx, rest[0])
	}
	return false, nil
}

// applyPackagedSelf lands the bundle this executable lives in.
func applyPackagedSelf(ctx context.Context) error {
	source, err := PackagedApplication()
	if err != nil {
		return err
	}
	target, err := helperclient.InstalledPath()
	if err != nil {
		return err
	}
	if source == target {
		return errors.New("cc-notes package: packaged and installed helper paths are identical")
	}
	if err := helperclient.EnsureApplicationDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	return helperdeployment.ApplyPackage(ctx, source)
}

// provisionRepository opens the installed runtime's business lane for one repo.
func provisionRepository(ctx context.Context, root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("cc-notes helper: repository path is not an exact absolute path")
	}
	appPath, err := helperclient.InstalledPath()
	if err != nil {
		return err
	}
	plan, err := helperdeployment.NewRuntimePlan(ctx, appPath, version.String())
	if err != nil {
		return err
	}
	return fusefs.ProvisionRepository(ctx, plan, root)
}
