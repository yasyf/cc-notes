//go:build darwin

package helperdeployment

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit"
	"github.com/yasyf/daemonkit/deploy"
	"github.com/yasyf/daemonkit/launchd"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/transportproto"
)

// DeploymentServiceLabel is the exact helper launch-agent label.
const DeploymentServiceLabel = helperclient.BundleID + ".fusekit"

// Every daemonkit verb these entry points reach budgets itself from the
// caller's context and refuses one carrying no deadline. The CLI's context
// carries none, so each verb states the whole budget it is worth here.
const (
	applyPackageBudget      = 10 * time.Minute
	activateServiceBudget   = 5 * time.Minute
	deactivateServiceBudget = 2 * time.Minute
	uninstallPackageBudget  = 5 * time.Minute
	runtimePlanBudget       = 2 * time.Minute
)

func budgeted(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if _, stated := ctx.Deadline(); stated {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, budget)
}

// helperDaemon mirrors what holder.New serves from the same plan: the label,
// schema, and trust the launcher opens against must be the ones the runtime
// answers with, or neither half finds the other.
func helperDaemon(appPath string) (daemonkit.Daemon, error) {
	program, err := daemonkit.InBundle(
		appPath, filepath.Join("Contents", "MacOS", helperclient.ExecutableName),
	)
	if err != nil {
		return daemonkit.Daemon{}, fmt.Errorf("cc-notes helper: resolve bundled program: %w", err)
	}
	daemon := stopDaemon()
	daemon.Program = program
	return daemon, nil
}

// stopDaemon names the helper daemon by label alone. A stated program adds
// Stop's executable-scoped inventory gate, which meets a live pre-v0.21 runtime
// and refuses the whole removal instead of taking its LaunchAgent down.
func stopDaemon() daemonkit.Daemon {
	requirement := helperclient.Requirement()
	return daemonkit.Daemon{
		Label:   DeploymentServiceLabel,
		Schemas: []daemonkit.Schema{daemonkit.Schema(transportproto.WireBuild)},
		Trust: daemonkit.Trust{
			Control:  &requirement,
			Business: daemonkit.Requirements{requirement},
			Serving:  daemonkit.ServingSigned(requirement),
		},
		Restart:  daemonkit.RestartAlways,
		Shutdown: daemonkit.Grace(helpercontract.RuntimeShutdownTimeout),
	}
}

func deploymentPlan(appPath string) (holder.DeploymentPlan, error) {
	runtimeDirectory, err := RuntimeDirectory()
	if err != nil {
		return holder.DeploymentPlan{}, err
	}
	presentationRoot, err := PresentationRoot()
	if err != nil {
		return holder.DeploymentPlan{}, err
	}
	return holder.NewDeploymentPlan(DeploymentPlanSpec(
		appPath, runtimeDirectory, presentationRoot, version.String(), runtimePolicyDigest(),
	))
}

func exactAgents(appPath string) ([]launchd.Agent, error) {
	plan, err := deploymentPlan(appPath)
	if err != nil {
		return nil, fmt.Errorf("cc-notes helper: derive deployment plan: %w", err)
	}
	return []launchd.Agent{plan.Agent()}, nil
}

func openDeployment(appPath string) (*deploy.Deployment, error) {
	agents, err := exactAgents(appPath)
	if err != nil {
		return nil, err
	}
	daemon, err := helperDaemon(appPath)
	if err != nil {
		return nil, err
	}
	return deploy.Open(deploy.Config{
		App: appPath, Requirement: helperclient.Requirement(), Daemon: daemon, Agents: agents,
	})
}

// ApplyPackage installs and activates one exact delivered helper candidate.
func ApplyPackage(ctx context.Context, source string) error {
	ctx, cancel := budgeted(ctx, applyPackageBudget)
	defer cancel()
	target, err := helperclient.InstalledPath()
	if err != nil {
		return err
	}
	if source == target {
		return errors.New("cc-notes package: delivered source and installed target must differ")
	}
	marketingVersion, err := helperclient.MarketingVersion()
	if err != nil {
		return err
	}
	digest, err := bundleTreeDigest(source)
	if err != nil {
		return err
	}
	deployment, err := openDeployment(target)
	if err != nil {
		return fmt.Errorf("cc-notes package: open deployment: %w", err)
	}
	land := deployment.Install
	switch _, err := os.Lstat(target); {
	case err == nil:
		land = deployment.Supersede
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("cc-notes package: inspect %q: %w", target, err)
	}
	generation, err := land(ctx, deploy.Candidate{
		Source: source, Version: marketingVersion, Digest: digest,
	})
	if err != nil {
		return fmt.Errorf("cc-notes package: land delivered app: %w", err)
	}
	if err := verifyGenerationFUSE(ctx, generation); err != nil {
		return err
	}
	activation, err := deployment.Activate(ctx)
	if err != nil {
		return fmt.Errorf("cc-notes package: activate installed app: %w", err)
	}
	return validateActivation(activation, target, marketingVersion)
}

// ActivateService activates the exact installed helper generation.
func ActivateService(ctx context.Context) error {
	ctx, cancel := budgeted(ctx, activateServiceBudget)
	defer cancel()
	target, err := helperclient.InstalledPath()
	if err != nil {
		return err
	}
	deployment, err := openDeployment(target)
	if err != nil {
		return fmt.Errorf("cc-notes helper: open deployment: %w", err)
	}
	activation, err := deployment.Activate(ctx)
	if err != nil {
		return fmt.Errorf("cc-notes helper: activate installed app: %w", err)
	}
	marketingVersion, err := helperclient.MarketingVersion()
	if err != nil {
		return err
	}
	if err := verifyGenerationFUSE(ctx, activation.Generation); err != nil {
		return err
	}
	return validateActivation(activation, target, marketingVersion)
}

// DeactivateService drains the installed helper runtime and removes its agent.
func DeactivateService(ctx context.Context) error {
	ctx, cancel := budgeted(ctx, deactivateServiceBudget)
	defer cancel()
	client, err := daemonkit.Open(stopDaemon())
	if err != nil {
		return fmt.Errorf("cc-notes helper: open signed helper: %w", err)
	}
	if err := client.Stop(ctx); err != nil {
		return fmt.Errorf("cc-notes helper: stop installed helper: %w", err)
	}
	return nil
}

// UninstallPackage deactivates and removes the controller-sealed helper generation.
func UninstallPackage(ctx context.Context) error {
	ctx, cancel := budgeted(ctx, uninstallPackageBudget)
	defer cancel()
	target, err := helperclient.InstalledPath()
	if err != nil {
		return err
	}
	deployment, err := openDeployment(target)
	if err != nil {
		return fmt.Errorf("cc-notes package: open deployment: %w", err)
	}
	removal, err := deployment.Uninstall(ctx)
	if err != nil {
		return fmt.Errorf("cc-notes package: uninstall installed app: %w", err)
	}
	if err := DeactivateService(ctx); err != nil {
		return err
	}
	if !removal.Runtime.Absent() || removal.Runtime.Digest() == (deploy.SHA256{}) {
		return errors.New("cc-notes package: daemonkit returned an inexact absence proof")
	}
	return validateGeneration(removal.Generation, target, removal.Generation.Version)
}

func validateActivation(activation deploy.Activation, appPath, marketingVersion string) error {
	if err := validateGeneration(activation.Generation, appPath, marketingVersion); err != nil {
		return err
	}
	if activation.Readiness.Build() != version.String() || activation.Readiness.Generation() == 0 ||
		activation.Readiness.Digest() == (deploy.SHA256{}) {
		return errors.New("cc-notes helper: daemonkit returned an inexact readiness proof")
	}
	return nil
}

func validateGeneration(generation deploy.Generation, appPath, marketingVersion string) error {
	if generation.Path != appPath || generation.Version != marketingVersion ||
		generation.TeamID != helperclient.TeamID ||
		generation.SigningIdentifier != helperclient.BundleID ||
		generation.CDHash == "" || generation.BundleDigest == "" ||
		generation.EntitlementsDigest == "" || generation.FileID == (deploy.FileID{}) {
		return errors.New("cc-notes helper: deployment receipt names a different helper generation")
	}
	return nil
}

func verifyGenerationFUSE(ctx context.Context, generation deploy.Generation) error {
	entitlements, err := deploy.ParseSHA256(generation.EntitlementsDigest)
	if err != nil {
		return fmt.Errorf("cc-notes helper: parse generation entitlement digest: %w", err)
	}
	return verifyPackagedFUSE(ctx, generation.Path, entitlements)
}

// bundleTreeDigest reproduces the tree digest deploy hashes a candidate bundle
// to, which deploy.Candidate requires and daemonkit v0.21.3 exports no way to
// compute. It is a hint, never an authority: Install and Supersede re-derive
// the digest themselves and refuse with deploy.ErrConflict on any
// disagreement, so a drift here fails the install loudly instead of admitting
// anything.
//
// TODO: delete this once daemonkit exports the digest (deploy.BundleDigest).
func bundleTreeDigest(root string) (deploy.SHA256, error) {
	digest := sha256.New()
	handle, err := os.OpenRoot(root)
	if err != nil {
		return deploy.SHA256{}, fmt.Errorf("cc-notes package: open bundle root: %w", err)
	}
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		writeDigestField(digest, filepath.ToSlash(relative))
		writeDigestField(digest, fmt.Sprintf("%#o", uint32(info.Mode())))
		switch {
		case info.IsDir():
			writeDigestField(digest, "directory")
			return nil
		case info.Mode().IsRegular():
			writeDigestField(digest, "regular")
			file, err := handle.Open(relative)
			if err != nil {
				return err
			}
			content := sha256.New()
			size, copyErr := io.Copy(content, file)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
			writeDigestField(digest, fmt.Sprintf("%d", size))
			writeDigestField(digest, hex.EncodeToString(content.Sum(nil)))
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			writeDigestField(digest, "symlink")
			target, err := handle.Readlink(relative)
			if err != nil {
				return err
			}
			writeDigestField(digest, target)
			return nil
		default:
			return fmt.Errorf("cc-notes package: bundle tree contains unsupported entry %q", path)
		}
	})
	if err := errors.Join(walkErr, handle.Close()); err != nil {
		return deploy.SHA256{}, fmt.Errorf("cc-notes package: digest bundle tree: %w", err)
	}
	var result deploy.SHA256
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func writeDigestField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}
