//go:build darwin

// Package helperpackage installs the fixed signed helper already delivered with cc-notes.
package helperpackage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/worker"
)

const (
	packagedDirectory = "libexec"
	copyTimeout       = 2 * time.Minute
)

type operations struct {
	packagedPath  func() (string, error)
	installedPath func() (string, error)
	copyApp       func(context.Context, string, string) error
	verifyCopy    func(context.Context, string, string) error
	activate      func(context.Context) error
	deactivate    func(context.Context) error
	rename        func(string, string) error
	removeAll     func(string) error
}

var defaultOperations = operations{
	packagedPath:  PackagedPath,
	installedPath: helperclient.InstalledPath,
	copyApp:       copyApp,
	verifyCopy:    verifyMatchingApps,
	activate:      helperclient.ActivateService,
	deactivate:    helperclient.DeactivateService,
	rename:        os.Rename,
	removeAll:     os.RemoveAll,
}

// PackagedPath returns the helper bundled beside the resolved cc-notes executable.
func PackagedPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cc-notes package: resolve executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("cc-notes package: resolve executable links: %w", err)
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return "", errors.New("cc-notes package: executable is not an exact absolute path")
	}
	prefix := filepath.Dir(filepath.Dir(executable))
	return filepath.Join(prefix, packagedDirectory, helperclient.ExecutableName+".app"), nil
}

// Install verifies, atomically replaces, and activates the delivered helper generation.
func Install(ctx context.Context) error {
	return install(ctx, defaultOperations)
}

func install(ctx context.Context, ops operations) error {
	source, err := ops.packagedPath()
	if err != nil {
		return err
	}
	target, err := ops.installedPath()
	if err != nil {
		return err
	}
	if source == target {
		return errors.New("cc-notes package: packaged and installed helper paths are identical")
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("cc-notes package: create application directory: %w", err)
	}
	stageRoot, err := os.MkdirTemp(parent, ".cc-notes-helper-stage-")
	if err != nil {
		return fmt.Errorf("cc-notes package: create staging directory: %w", err)
	}
	defer func() { _ = ops.removeAll(stageRoot) }()
	staged := filepath.Join(stageRoot, filepath.Base(target))
	if err := ops.copyApp(ctx, source, staged); err != nil {
		return fmt.Errorf("cc-notes package: stage signed helper: %w", err)
	}
	if err := ops.verifyCopy(ctx, source, staged); err != nil {
		return fmt.Errorf("cc-notes package: verify staged helper: %w", err)
	}

	present, err := regularAppPresent(target)
	if err != nil {
		return err
	}
	var backupRoot, backup string
	if present {
		if err := ops.deactivate(ctx); err != nil {
			return fmt.Errorf("cc-notes package: deactivate installed helper: %w", err)
		}
		backupRoot, err = os.MkdirTemp(parent, ".cc-notes-helper-backup-")
		if err != nil {
			return fmt.Errorf("cc-notes package: create rollback directory: %w", err)
		}
		defer func() { _ = ops.removeAll(backupRoot) }()
		backup = filepath.Join(backupRoot, filepath.Base(target))
		if err := ops.rename(target, backup); err != nil {
			return errors.Join(
				fmt.Errorf("cc-notes package: preserve installed helper: %w", err),
				reactivate(ctx, ops, target),
			)
		}
	}
	if err := ops.rename(staged, target); err != nil {
		return errors.Join(
			fmt.Errorf("cc-notes package: publish staged helper: %w", err),
			restorePrevious(ctx, ops, target, backup),
		)
	}
	if err := ops.activate(ctx); err != nil {
		return errors.Join(
			fmt.Errorf("cc-notes package: activate installed helper: %w", err),
			rollbackPublished(ctx, ops, stageRoot, target, backup),
		)
	}
	return nil
}

// Uninstall deactivates the exact installed generation before removing its app bundle.
func Uninstall(ctx context.Context) error {
	return uninstall(ctx, defaultOperations)
}

func uninstall(ctx context.Context, ops operations) error {
	target, err := ops.installedPath()
	if err != nil {
		return err
	}
	present, err := regularAppPresent(target)
	if err != nil || !present {
		return err
	}
	if err := ops.deactivate(ctx); err != nil {
		return fmt.Errorf("cc-notes package: deactivate installed helper: %w", err)
	}
	removedRoot, err := os.MkdirTemp(filepath.Dir(target), ".cc-notes-helper-remove-")
	if err != nil {
		return fmt.Errorf("cc-notes package: create removal directory: %w", err)
	}
	removed := filepath.Join(removedRoot, filepath.Base(target))
	if err := ops.rename(target, removed); err != nil {
		_ = ops.removeAll(removedRoot)
		return fmt.Errorf("cc-notes package: withdraw installed helper: %w", err)
	}
	if err := ops.removeAll(removedRoot); err != nil {
		return fmt.Errorf("cc-notes package: remove installed helper: %w", err)
	}
	return nil
}

func regularAppPresent(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cc-notes package: inspect installed helper: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.New("cc-notes package: installed helper is not a regular application directory")
	}
	return true, nil
}

func copyApp(ctx context.Context, source, destination string) error {
	runner, err := helperclient.NewToolRunner(ctx)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = runner.Close(closeCtx)
	}()
	_, err = runner.Run(ctx, worker.CommandRequest{
		Path: "/usr/bin/ditto", Args: []string{"--noqtn", source, destination}, TotalTimeout: copyTimeout,
	})
	return err
}

func verifyMatchingApps(ctx context.Context, source, staged string) error {
	version, err := helperclient.MarketingVersion()
	if err != nil {
		return err
	}
	controller := deployment.New()
	sourceAttestation, err := controller.AttestInstalled(ctx, deployment.InstalledSpec{
		AppPath: source, Version: version, Identity: helperclient.CodeIdentity(),
	})
	if err != nil {
		return fmt.Errorf("attest delivered app: %w", err)
	}
	stagedAttestation, err := controller.AttestInstalled(ctx, deployment.InstalledSpec{
		AppPath: staged, Version: version, Identity: helperclient.CodeIdentity(),
	})
	if err != nil {
		return fmt.Errorf("attest staged app: %w", err)
	}
	if sourceAttestation.Version() != stagedAttestation.Version() ||
		sourceAttestation.TeamID() != stagedAttestation.TeamID() ||
		sourceAttestation.SigningIdentifier() != stagedAttestation.SigningIdentifier() ||
		sourceAttestation.DesignatedRequirement() != stagedAttestation.DesignatedRequirement() ||
		sourceAttestation.CDHash() != stagedAttestation.CDHash() ||
		sourceAttestation.BundleDigest() != stagedAttestation.BundleDigest() ||
		sourceAttestation.EntitlementsDigest() != stagedAttestation.EntitlementsDigest() {
		return errors.New("delivered and staged helper generations differ")
	}
	return nil
}

func rollbackPublished(ctx context.Context, ops operations, stageRoot, target, backup string) error {
	deactivateErr := ops.deactivate(ctx)
	failed := filepath.Join(stageRoot, filepath.Base(target))
	withdrawErr := ops.rename(target, failed)
	if withdrawErr != nil {
		return errors.Join(fmt.Errorf("withdraw failed generation: %w", withdrawErr), deactivateErr)
	}
	return errors.Join(deactivateErr, restorePrevious(ctx, ops, target, backup))
}

func restorePrevious(ctx context.Context, ops operations, target, backup string) error {
	if backup == "" {
		return nil
	}
	if err := ops.rename(backup, target); err != nil {
		return fmt.Errorf("restore prior helper: %w", err)
	}
	return reactivate(ctx, ops, target)
}

func reactivate(ctx context.Context, ops operations, target string) error {
	present, err := regularAppPresent(target)
	if err != nil || !present {
		return err
	}
	if err := ops.activate(ctx); err != nil {
		return fmt.Errorf("reactivate prior helper: %w", err)
	}
	return nil
}
