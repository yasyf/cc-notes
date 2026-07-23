package helperclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/daemonkit/trust"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/transportproto"
)

const (
	consumerBuildDomain      = "cc-notes.deployment-callbacks.v1@sha256:"
	deploymentPolicyIdentity = "cc-notes.deployment-callbacks.v1"
	// DeploymentProofIdentity is the v1 product-proof digest domain.
	DeploymentProofIdentity = "cc-notes.deployment-proof.v1"
	// DeploymentStopRole is the exact disposable stop-control role.
	DeploymentStopRole = BundleID + ".stop-control"
	// DeploymentServiceLabel is the exact helper launch-agent label.
	DeploymentServiceLabel = BundleID + ".fusekit"
)

var (
	startupConsumerBuild, startupConsumerBuildErr = currentConsumerBuild()
	startupPolicyDigest, startupPolicyDigestErr   = makeDeploymentPolicyDigest()
)

type deploymentPolicy struct {
	Identity    string                      `json:"identity"`
	Schema      uint16                      `json:"schema"`
	Application deploymentApplicationPolicy `json:"application"`
	Protocols   deploymentProtocolPolicy    `json:"protocols"`
	Runtime     deploymentRuntimePolicy     `json:"runtime"`
	Service     deploymentServicePolicy     `json:"service"`
}

type deploymentApplicationPolicy struct {
	BundleID                    string `json:"bundle_id"`
	TeamID                      string `json:"team_id"`
	InstallRootHomeRelative     string `json:"install_root_home_relative"`
	BundleLeaf                  string `json:"bundle_leaf"`
	ExecutableName              string `json:"executable_name"`
	ExecutableRelativePath      string `json:"executable_relative_path"`
	RequireCanonicalAccountHome bool   `json:"require_canonical_account_home"`
	StopControlRole             string `json:"stop_control_role"`
}

type deploymentProtocolPolicy struct {
	MountProtocol   uint16 `json:"mount_protocol"`
	RuntimeProtocol uint16 `json:"runtime_protocol"`
	WireProtocol    uint16 `json:"wire_protocol"`
	WireBuild       string `json:"wire_build"`
}

type deploymentRuntimePolicy struct {
	State     deploymentRuntimeStatePolicy  `json:"state"`
	Native    deploymentNativePolicy        `json:"native"`
	Source    deploymentSourcePolicy        `json:"source"`
	Broker    deploymentBrokerPolicy        `json:"broker"`
	Budgets   deploymentRuntimeBudgetPolicy `json:"budgets"`
	Readiness deploymentReadinessPolicy     `json:"readiness"`
}

type deploymentRuntimeBudgetPolicy struct {
	NativeReadinessTimeout  time.Duration `json:"native_readiness_timeout_ns"`
	SourceReadinessTimeout  time.Duration `json:"source_readiness_timeout_ns"`
	CatalogReadinessTimeout time.Duration `json:"catalog_readiness_timeout_ns"`
	CatalogOperationTimeout time.Duration `json:"catalog_operation_timeout_ns"`
	ShutdownTimeout         time.Duration `json:"shutdown_timeout_ns"`
}

type deploymentRuntimeStatePolicy struct {
	HomeRelativeDirectory    string `json:"home_relative_directory"`
	SocketName               string `json:"socket_name"`
	CatalogName              string `json:"catalog_name"`
	ProcessStoreName         string `json:"process_store_name"`
	LogName                  string `json:"log_name"`
	SourceObserverDirectory  string `json:"source_observer_directory_pattern"`
	SourceObserverSocketName string `json:"source_observer_socket_name"`
	RuntimePolicyDigest      string `json:"runtime_policy_digest"`
}

type deploymentNativePolicy struct {
	Enabled                      bool                   `json:"enabled"`
	PresentationRootHomeRelative string                 `json:"presentation_root_home_relative"`
	RequiredPhase                mountproto.NativePhase `json:"required_phase"`
	Filesystem                   string                 `json:"filesystem"`
	FUSE                         deploymentFUSEPolicy   `json:"fuse"`
}

type deploymentFUSEPolicy struct {
	ManifestVersion                int      `json:"manifest_version"`
	SourceSHA256                   string   `json:"source_sha256"`
	LicenseSHA256                  string   `json:"license_sha256"`
	InstallName                    string   `json:"install_name"`
	LibraryRelativePath            string   `json:"library_relative_path"`
	LicenseRelativePath            string   `json:"license_relative_path"`
	ManifestRelativePath           string   `json:"manifest_relative_path"`
	Architectures                  []string `json:"architectures"`
	Dependencies                   []string `json:"dependencies"`
	NestedSigningIdentifier        string   `json:"nested_signing_identifier"`
	RequireSignedLibraryDigest     bool     `json:"require_signed_library_digest"`
	RequireOuterEntitlementsDigest bool     `json:"require_outer_entitlements_digest"`
	RequireStrictBundleDescendants bool     `json:"require_strict_bundle_descendants"`
	RequireRegularNonSymlinkFiles  bool     `json:"require_regular_non_symlink_files"`
	RequireExactNestedRequirement  bool     `json:"require_exact_nested_requirement"`
	RequireExactOuterRequirement   bool     `json:"require_exact_outer_requirement"`
	RequireNestedHardenedRuntime   bool     `json:"require_nested_hardened_runtime"`
	RequireOuterHardenedRuntime    bool     `json:"require_outer_hardened_runtime"`
	ForbiddenEntitlementsScope     string   `json:"forbidden_entitlements_scope"`
	ForbiddenInjectionEntitlements []string `json:"forbidden_injection_entitlements"`
}

type deploymentSourcePolicy struct {
	Capable bool `json:"capable"`
}

type deploymentBrokerPolicy struct {
	Enabled bool `json:"enabled"`
}

type deploymentReadinessPolicy struct {
	StartupTimeout               time.Duration             `json:"startup_timeout_ns"`
	SettlementTimeout            time.Duration             `json:"settlement_timeout_ns"`
	ObservationTimeout           time.Duration             `json:"observation_timeout_ns"`
	RequiredState                mountproto.RuntimeState   `json:"required_state"`
	RequiredPhase                mountproto.ReadinessPhase `json:"required_phase"`
	RequiredStep                 mountproto.ReadinessStep  `json:"required_step"`
	RequireReady                 bool                      `json:"require_ready"`
	RequireNotDraining           bool                      `json:"require_not_draining"`
	RequireNotBusy               bool                      `json:"require_not_busy"`
	RequireRuntimeBuildMatch     bool                      `json:"require_runtime_build_match"`
	RequirePositiveRuntimePID    bool                      `json:"require_positive_runtime_pid"`
	RequireProcessGeneration     bool                      `json:"require_process_generation"`
	RequireActivationGeneration  bool                      `json:"require_activation_generation"`
	RequireEmptyMessage          bool                      `json:"require_empty_message"`
	RequiredErrorCode            mountproto.ErrorCode      `json:"required_error_code"`
	RequireNativeProof           bool                      `json:"require_native_proof"`
	RequirePresentationRoot      bool                      `json:"require_presentation_root_match"`
	RequireNativeSource          bool                      `json:"require_native_source_match"`
	RequirePositiveRootReadEpoch bool                      `json:"require_positive_root_read_epoch"`
	RequiredBrokerPhase          mountproto.BrokerPhase    `json:"required_broker_phase"`
}

type deploymentServicePolicy struct {
	AgentLabel                     string                  `json:"agent_label"`
	ExactSingleAgentPlan           bool                    `json:"exact_single_agent_plan"`
	AssociatedBundleID             string                  `json:"associated_bundle_id"`
	RestartPolicy                  service.RestartPolicy   `json:"restart_policy"`
	SessionType                    service.SessionType     `json:"session_type"`
	StartInterval                  time.Duration           `json:"start_interval_ns"`
	ProcessType                    service.ProcessType     `json:"process_type"`
	ProgramIsFixedBundleExecutable bool                    `json:"program_is_fixed_bundle_executable"`
	RequireNoArguments             bool                    `json:"require_no_arguments"`
	RequireNoWatchPaths            bool                    `json:"require_no_watch_paths"`
	RequireNoCalendarIntervals     bool                    `json:"require_no_calendar_intervals"`
	BuildEnvironmentKey            string                  `json:"build_environment_key"`
	RequireExactBuildEnvironment   bool                    `json:"require_exact_build_environment"`
	ReplacementOwnsRestartFence    bool                    `json:"replacement_owns_restart_fence"`
	Proofs                         deploymentProofPolicy   `json:"proofs"`
	Quiesce                        deploymentQuiescePolicy `json:"quiesce"`
}

type deploymentProofPolicy struct {
	PostInstallRole                       deployment.ProofRole `json:"post_install_role"`
	CandidateReadyRole                    deployment.ProofRole `json:"candidate_ready_role"`
	PriorRestoreRole                      deployment.ProofRole `json:"prior_restore_role"`
	PriorReadyRole                        deployment.ProofRole `json:"prior_ready_role"`
	PriorRuntimeRole                      deployment.ProofRole `json:"prior_runtime_role"`
	RollbackRuntimeRole                   deployment.ProofRole `json:"rollback_runtime_role"`
	RequireReturnedRoleMatch              bool                 `json:"require_returned_role_match"`
	RequireReadinessPlanDigestMatch       bool                 `json:"require_readiness_plan_digest_match"`
	GenerationBindingIncludesCDHash       bool                 `json:"generation_binding_includes_cdhash"`
	GenerationBindingIncludesBundleDigest bool                 `json:"generation_binding_includes_bundle_digest"`
}

type deploymentQuiescePolicy struct {
	ProofIdentity                   string               `json:"proof_identity"`
	StopControlArguments            []string             `json:"stop_control_arguments"`
	ReplacementStopIntent           wire.StopIntent      `json:"replacement_stop_intent"`
	ReconfigureStopIntent           wire.StopIntent      `json:"reconfigure_stop_intent"`
	DeactivateStopIntent            wire.StopIntent      `json:"deactivate_stop_intent"`
	RollbackStopIntent              wire.StopIntent      `json:"rollback_stop_intent"`
	PriorRuntimeProofRole           deployment.ProofRole `json:"prior_runtime_proof_role"`
	RollbackRuntimeProofRole        deployment.ProofRole `json:"rollback_runtime_proof_role"`
	RuntimeBuildSource              string               `json:"runtime_build_source"`
	RequireTargetProcessGeneration  bool                 `json:"require_target_process_generation"`
	RequireExactExecutableInventory bool                 `json:"require_exact_executable_inventory"`
	AbsentRequiresEmptyInventory    bool                 `json:"absent_requires_empty_inventory"`
	RequireExactHealthTarget        bool                 `json:"require_exact_health_target"`
	RequireExactStopResult          bool                 `json:"require_exact_stop_result"`
}

// DeploymentIdentity returns the startup-frozen updater build and callback
// policy identities used by helper release deployment.
func DeploymentIdentity() (string, deployment.SHA256, error) {
	if startupConsumerBuildErr != nil {
		return "", deployment.SHA256{}, fmt.Errorf("cc-notes helper: cache deployment consumer build: %w", startupConsumerBuildErr)
	}
	if startupPolicyDigestErr != nil {
		return "", deployment.SHA256{}, fmt.Errorf("cc-notes helper: cache deployment policy digest: %w", startupPolicyDigestErr)
	}
	return startupConsumerBuild, startupPolicyDigest, nil
}

func currentConsumerBuild() (string, error) {
	path, err := service.CanonicalExecutable()
	if err != nil {
		return "", err
	}
	return consumerBuildForExecutable(path)
}

func consumerBuildForExecutable(path string) (build string, returnErr error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("cc-notes helper: updater executable path is not exact and absolute")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("cc-notes helper: open updater directory: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil && returnErr == nil {
			build = ""
			returnErr = fmt.Errorf("cc-notes helper: close updater directory: %w", err)
		}
	}()
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("cc-notes helper: open updater executable: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil && returnErr == nil {
			build = ""
			returnErr = fmt.Errorf("cc-notes helper: close updater executable: %w", err)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("cc-notes helper: inspect updater executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("cc-notes helper: updater executable is not an executable regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("cc-notes helper: hash updater executable: %w", err)
	}
	return consumerBuildDomain + hex.EncodeToString(digest.Sum(nil)), nil
}

func makeDeploymentPolicyDigest() (deployment.SHA256, error) {
	payload, err := deploymentPolicyJSON()
	if err != nil {
		return deployment.SHA256{}, err
	}
	return deployment.SHA256(sha256.Sum256(payload)), nil
}

func deploymentPolicyJSON() ([]byte, error) {
	readiness := holder.StandardReadinessContract()
	runtimePolicy, err := (trust.Requirement{
		TeamID: TeamID, SigningIdentifier: BundleID,
	}).ValidationDigest()
	if err != nil {
		return nil, err
	}
	return json.Marshal(deploymentPolicy{
		Identity: deploymentPolicyIdentity,
		Schema:   1,
		Application: deploymentApplicationPolicy{
			BundleID: BundleID, TeamID: TeamID, InstallRootHomeRelative: "Applications",
			BundleLeaf: ExecutableName + ".app", ExecutableName: ExecutableName,
			ExecutableRelativePath:      "Contents/MacOS/" + ExecutableName,
			RequireCanonicalAccountHome: true,
			StopControlRole:             DeploymentStopRole,
		},
		Protocols: deploymentProtocolPolicy{
			MountProtocol: mountproto.Version, RuntimeProtocol: mountproto.RuntimeProtocolVersion,
			WireProtocol: transportproto.Version, WireBuild: transportproto.WireBuild,
		},
		Runtime: deploymentRuntimePolicy{
			State: deploymentRuntimeStatePolicy{
				HomeRelativeDirectory: ".cc-notes/fusekit-v1", SocketName: "fusekit.sock",
				CatalogName: "catalog.sqlite", ProcessStoreName: "processes.db", LogName: "holder.log",
				SourceObserverDirectory: "source-observer-0000000000", SourceObserverSocketName: "observer.sock",
				RuntimePolicyDigest: hex.EncodeToString(runtimePolicy[:]),
			},
			Native: deploymentNativePolicy{
				Enabled: true, PresentationRootHomeRelative: ".cc-notes/mnt",
				RequiredPhase: mountproto.NativePhaseLive, Filesystem: mountproto.NativeMountFilesystem,
				FUSE: deploymentFUSEPolicy{
					ManifestVersion: 1, SourceSHA256: holder.FUSESourceSHA256,
					LicenseSHA256: holder.FUSELicenseSHA256, InstallName: holder.FUSEInstallName,
					LibraryRelativePath:  holder.FUSELibraryRelativePath,
					LicenseRelativePath:  holder.FUSELicenseRelativePath,
					ManifestRelativePath: holder.FUSEManifestRelativePath,
					Architectures:        []string{"arm64", "x86_64"},
					Dependencies: []string{
						"/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation",
						"/System/Library/Frameworks/DiskArbitration.framework/Versions/A/DiskArbitration",
						"/usr/lib/libSystem.B.dylib", "/usr/lib/libiconv.2.dylib",
					},
					NestedSigningIdentifier:    BundleID + ".fuse-t",
					RequireSignedLibraryDigest: true, RequireOuterEntitlementsDigest: true,
					RequireStrictBundleDescendants: true, RequireRegularNonSymlinkFiles: true,
					RequireExactNestedRequirement: true, RequireExactOuterRequirement: true,
					RequireNestedHardenedRuntime: true, RequireOuterHardenedRuntime: true,
					ForbiddenEntitlementsScope: "outer_and_nested",
					ForbiddenInjectionEntitlements: []string{
						"com.apple.security.cs.disable-library-validation",
						"com.apple.security.cs.allow-dyld-environment-variables",
						"com.apple.security.cs.allow-unsigned-executable-memory",
						"com.apple.security.cs.allow-jit",
						"com.apple.security.cs.disable-executable-page-protection",
						"com.apple.security.get-task-allow",
					},
				},
			},
			Source: deploymentSourcePolicy{Capable: true},
			Broker: deploymentBrokerPolicy{Enabled: false},
			Budgets: deploymentRuntimeBudgetPolicy{
				NativeReadinessTimeout:  helpercontract.RuntimeNativeReadinessTimeout,
				SourceReadinessTimeout:  helpercontract.RuntimeSourceReadinessTimeout,
				CatalogReadinessTimeout: helpercontract.RuntimeCatalogReadinessTimeout,
				CatalogOperationTimeout: helpercontract.RuntimeCatalogOperationTimeout,
				ShutdownTimeout:         helpercontract.RuntimeShutdownTimeout,
			},
			Readiness: deploymentReadinessPolicy{
				StartupTimeout: readiness.StartupTimeout(), SettlementTimeout: readiness.SettlementTimeout(),
				ObservationTimeout: readiness.ObservationTimeout(), RequiredState: mountproto.RuntimeStateHealthy,
				RequiredPhase: mountproto.ReadinessPhaseReady, RequiredStep: mountproto.ReadinessStepPublished,
				RequireReady: true, RequireNotDraining: true, RequireNotBusy: true,
				RequireRuntimeBuildMatch: true, RequirePositiveRuntimePID: true,
				RequireProcessGeneration: true, RequireActivationGeneration: true,
				RequireEmptyMessage: true, RequiredErrorCode: mountproto.ErrorCodeOk,
				RequireNativeProof: true, RequirePresentationRoot: true, RequireNativeSource: true,
				RequirePositiveRootReadEpoch: true, RequiredBrokerPhase: mountproto.BrokerPhaseDisabled,
			},
		},
		Service: deploymentServicePolicy{
			AgentLabel: DeploymentServiceLabel, ExactSingleAgentPlan: true, AssociatedBundleID: BundleID,
			RestartPolicy: service.RestartAlways, SessionType: service.SessionTypeAqua,
			StartInterval: 0, ProcessType: 0,
			ProgramIsFixedBundleExecutable: true, RequireNoArguments: true,
			RequireNoWatchPaths: true, RequireNoCalendarIntervals: true,
			BuildEnvironmentKey: "FUSEKIT_BUILD_ID", RequireExactBuildEnvironment: true,
			ReplacementOwnsRestartFence: true,
			Proofs: deploymentProofPolicy{
				PostInstallRole: deployment.ProofPostInstall, CandidateReadyRole: deployment.ProofCandidateReady,
				PriorRestoreRole: deployment.ProofPriorRestore, PriorReadyRole: deployment.ProofPriorReady,
				PriorRuntimeRole: deployment.ProofPriorRuntime, RollbackRuntimeRole: deployment.ProofRollbackRuntime,
				RequireReturnedRoleMatch: true, RequireReadinessPlanDigestMatch: true,
				GenerationBindingIncludesCDHash: true, GenerationBindingIncludesBundleDigest: true,
			},
			Quiesce: deploymentQuiescePolicy{
				ProofIdentity: DeploymentProofIdentity, StopControlArguments: holder.StopControlChildArguments(),
				ReplacementStopIntent: wire.StopIntentUpgrade, ReconfigureStopIntent: wire.StopIntentRestart,
				DeactivateStopIntent: wire.StopIntentUninstall, RollbackStopIntent: wire.StopIntentUninstall,
				PriorRuntimeProofRole:           deployment.ProofPriorRuntime,
				RollbackRuntimeProofRole:        deployment.ProofRollbackRuntime,
				RuntimeBuildSource:              "invoking_build",
				RequireTargetProcessGeneration:  true,
				RequireExactExecutableInventory: true, AbsentRequiresEmptyInventory: true,
				RequireExactHealthTarget: true, RequireExactStopResult: true,
			},
		},
	})
}
