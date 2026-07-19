package fusefs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/tenant"
)

const (
	sourceWorkerMode    = "cc-notes-source-mutation-v1"
	sourceWorkerVersion = 1
)

type sourceMutationPlanner struct {
	executable string
	tenants    map[catalog.TenantID]Tenant
}

func (p sourceMutationPlanner) PrepareSourceMutation(
	_ context.Context,
	step tenant.SourceMutationStep,
) (tenant.SourceMutationWorker, error) {
	configured, ok := p.tenants[step.TenantID]
	if !ok || configured.Generation != step.Generation || string(configured.Authority) != step.SourceID {
		return tenant.SourceMutationWorker{}, errors.New("cc-notes source: mutation does not match a configured tenant authority")
	}
	if step.OperationID == (catalog.MutationID{}) || step.ExpectedHead == 0 {
		return tenant.SourceMutationWorker{}, errors.New("cc-notes source: mutation identity is incomplete")
	}
	if err := validateMutationContext(configured, step.Source); err != nil {
		return tenant.SourceMutationWorker{}, err
	}
	config := sourceWorkerConfig{
		Tenant: step.TenantID, Generation: step.Generation, Revision: step.ExpectedHead,
		OperationID: step.OperationID, RepoRoot: configured.RepoRoot, Source: step.Source,
	}
	arguments, err := sourceWorkerArguments(config)
	if err != nil {
		return tenant.SourceMutationWorker{}, err
	}
	worker := tenant.SourceMutationWorker{
		OperationID: step.OperationID, SourceID: step.SourceID, SourceMetadata: step.SourceMetadata,
		Spec: tenant.WorkerSpec{
			Path: p.executable, Args: arguments, Dir: configured.RepoRoot,
			Env: sourceWorkerEnvironment(os.Environ(), step.OperationID.String()),
		},
	}
	if step.Kind == catalog.MutationCreate {
		kind, err := mutationKind(step.Source)
		if err != nil {
			return tenant.SourceMutationWorker{}, err
		}
		worker.SourceResult = &catalog.SourceLocator{
			SourceAuthority: step.Source.Parent.SourceAuthority,
			SourceKey:       catalog.SourceObjectKey(entitySourceKeyForNonce(kind, step.OperationID.String())),
			SourceRevision:  step.Source.Parent.SourceRevision,
		}
	}
	return worker, nil
}

type sourceWorkerConfig struct {
	Tenant      catalog.TenantID              `json:"tenant"`
	Generation  catalog.Generation            `json:"generation"`
	Revision    catalog.Revision              `json:"revision"`
	OperationID catalog.MutationID            `json:"operation_id"`
	RepoRoot    string                        `json:"repo_root"`
	Source      catalog.SourceMutationContext `json:"source"`
}

type sourceWorkerEnvelope struct {
	Protocol int                `json:"protocol"`
	Config   sourceWorkerConfig `json:"config"`
}

type sourceWorkerProof struct {
	Tenant     catalog.TenantID   `json:"tenant"`
	Generation catalog.Generation `json:"generation"`
	Revision   catalog.Revision   `json:"revision"`
	Lane       tenant.Lane        `json:"lane"`
}

func sourceWorkerArguments(config sourceWorkerConfig) ([]string, error) {
	if err := validateSourceWorkerConfig(config); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(sourceWorkerEnvelope{Protocol: sourceWorkerVersion, Config: config})
	if err != nil {
		return nil, fmt.Errorf("cc-notes source: encode worker arguments: %w", err)
	}
	return []string{sourceWorkerMode, base64.RawURLEncoding.EncodeToString(payload)}, nil
}

func parseSourceWorkerArguments(arguments []string) (sourceWorkerConfig, bool, error) {
	if len(arguments) == 0 || arguments[0] != sourceWorkerMode {
		return sourceWorkerConfig{}, false, nil
	}
	if len(arguments) != 2 {
		return sourceWorkerConfig{}, true, errors.New("cc-notes source: worker arguments are invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(arguments[1])
	if err != nil {
		return sourceWorkerConfig{}, true, fmt.Errorf("cc-notes source: decode worker arguments: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope sourceWorkerEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return sourceWorkerConfig{}, true, fmt.Errorf("cc-notes source: decode worker contract: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return sourceWorkerConfig{}, true, errors.New("cc-notes source: worker contract has trailing data")
	}
	if envelope.Protocol != sourceWorkerVersion {
		return sourceWorkerConfig{}, true, fmt.Errorf("cc-notes source: worker protocol %d is unsupported", envelope.Protocol)
	}
	if err := validateSourceWorkerConfig(envelope.Config); err != nil {
		return sourceWorkerConfig{}, true, err
	}
	return envelope.Config, true, nil
}

// RunHolderChild dispatches cc-notes source workers before FuseKit's native and materialization children.
func RunHolderChild(
	ctx context.Context,
	arguments []string,
	stdin io.Reader,
	stdout io.Writer,
) (bool, error) {
	config, handled, err := parseSourceWorkerArguments(arguments)
	if err != nil || handled {
		if err != nil {
			return true, err
		}
		return true, runSourceWorker(ctx, config, stdin, stdout)
	}
	return holder.RunChild(ctx, arguments, stdout)
}

func runSourceWorker(ctx context.Context, config sourceWorkerConfig, input io.Reader, output io.Writer) error {
	if err := validateSourceWorkerConfig(config); err != nil {
		return err
	}
	if output == nil {
		return errors.New("cc-notes source: worker proof output is required")
	}
	source, err := store.OpenContext(ctx, config.RepoRoot)
	if err != nil {
		return fmt.Errorf("cc-notes source: open repository: %w", err)
	}
	if err := applySourceMutation(ctx, source, config, input); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(sourceWorkerProof{
		Tenant: config.Tenant, Generation: config.Generation,
		Revision: config.Revision, Lane: tenant.LaneCatalogMutation,
	})
}

func applySourceMutation(ctx context.Context, source *store.Store, config sourceWorkerConfig, input io.Reader) error {
	operation := config.Source.Operation
	switch operation.Kind {
	case catalog.MutationCreate:
		kind, err := mutationKind(config.Source)
		if err != nil {
			return err
		}
		key := entitySourceKeyForNonce(kind, config.OperationID.String())
		if existing, ref, found, err := findSourceEntity(ctx, source, kind, key); err != nil {
			return err
		} else if found {
			applied, err := source.HasSession(ctx, ref, config.OperationID.String())
			if err != nil {
				return err
			}
			if !applied || existing.Meta().Deleted {
				return errors.New("cc-notes source: reserved create key already exists without the mutation session")
			}
			return nil
		}
		content, err := readMutationContent(input)
		if err != nil {
			return err
		}
		ops, err := codecOf(kind).New(content)
		if err != nil {
			return fmt.Errorf("cc-notes source: parse new %s: %w", kind, err)
		}
		ops[0], err = setCreateNonce(ops[0], config.OperationID.String())
		if err != nil {
			return err
		}
		_, err = source.CreateExact(ctx, ops)
		return err
	case catalog.MutationRevise:
		return reviseSourceEntity(ctx, source, config.Source.Object, config.OperationID.String(), input)
	case catalog.MutationDelete:
		return tombstoneSourceEntity(ctx, source, config.Source.Object, config.OperationID.String())
	case catalog.MutationReplace:
		if config.Source.Object == nil || config.Source.Target == nil || *config.Source.Object == *config.Source.Target {
			return errors.New("cc-notes source: replace locators are incomplete")
		}
		if operation.HasContent {
			if err := reviseSourceEntity(ctx, source, config.Source.Object, config.OperationID.String(), input); err != nil {
				return err
			}
		}
		return tombstoneSourceEntity(ctx, source, config.Source.Target, config.OperationID.String())
	default:
		return errors.New("cc-notes source: unsupported mutation kind")
	}
}

func reviseSourceEntity(
	ctx context.Context,
	source *store.Store,
	locator *catalog.SourceLocator,
	session string,
	input io.Reader,
) error {
	kind, key, err := entityLocator(locator)
	if err != nil {
		return err
	}
	snapshot, ref, found, err := findSourceEntity(ctx, source, kind, key)
	if err != nil {
		return err
	}
	if !found || snapshot.Meta().Deleted {
		return errors.New("cc-notes source: revised entity does not exist")
	}
	applied, err := source.HasSession(ctx, ref, session)
	if err != nil || applied {
		return err
	}
	content, err := readMutationContent(input)
	if err != nil {
		return err
	}
	ops, err := codecOf(kind).Diff(snapshot, content)
	if err != nil {
		return fmt.Errorf("cc-notes source: revise %s: %w", kind, err)
	}
	if len(ops) == 0 {
		return errors.New("cc-notes source: revision produced no durable operation")
	}
	_, err = source.Append(ctx, ref, ops)
	return err
}

func tombstoneSourceEntity(
	ctx context.Context,
	source *store.Store,
	locator *catalog.SourceLocator,
	session string,
) error {
	kind, key, err := entityLocator(locator)
	if err != nil {
		return err
	}
	snapshot, ref, found, err := findSourceEntity(ctx, source, kind, key)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("cc-notes source: deleted entity does not exist")
	}
	applied, err := source.HasSession(ctx, ref, session)
	if err != nil || applied {
		return err
	}
	if snapshot.Meta().Deleted {
		return errors.New("cc-notes source: entity was tombstoned by a different mutation")
	}
	_, err = source.Append(ctx, ref, []model.Op{model.DeleteNote{}})
	return err
}

func findSourceEntity(
	ctx context.Context,
	source *store.Store,
	kind model.Kind,
	key string,
) (model.Snapshot, string, bool, error) {
	rooted, err := source.ListRootedSnapshots(ctx, kind, store.ListOpts{IncludeDeleted: true, IncludeSuperseded: true})
	if err != nil {
		return nil, "", false, err
	}
	for _, candidate := range rooted {
		candidateKey, err := entitySourceKey(kind, candidate.Root)
		if err != nil {
			return nil, "", false, err
		}
		if candidateKey == key {
			return candidate.Snapshot, refs.For(kind, candidate.Snapshot.EntityID()), true, nil
		}
	}
	return nil, "", false, nil
}

func validateMutationContext(configured Tenant, source catalog.SourceMutationContext) error {
	if source.Operation.Kind < catalog.MutationCreate || source.Operation.Kind > catalog.MutationReplace {
		return errors.New("cc-notes source: mutation kind is invalid")
	}
	if source.Operation.ObjectKind != catalog.KindFile || !source.Operation.HasContent &&
		(source.Operation.Kind == catalog.MutationCreate || source.Operation.Kind == catalog.MutationRevise) {
		return errors.New("cc-notes source: only content-backed entity files are mutable")
	}
	for _, locator := range []*catalog.SourceLocator{source.Object, source.Parent, source.Target} {
		if locator != nil && string(locator.SourceAuthority) != string(configured.Authority) {
			return errors.New("cc-notes source: locator authority does not match the configured tenant")
		}
	}
	_, err := mutationKind(source)
	return err
}

func validateSourceWorkerConfig(config sourceWorkerConfig) error {
	if config.Tenant == "" || config.Generation == 0 || config.Revision == 0 ||
		config.OperationID == (catalog.MutationID{}) || !exactAbsolutePath(config.RepoRoot) {
		return errors.New("cc-notes source: worker contract is incomplete")
	}
	return validateMutationShape(config.Source)
}

func validateMutationShape(source catalog.SourceMutationContext) error {
	switch source.Operation.Kind {
	case catalog.MutationCreate:
		if source.Parent == nil || source.Object != nil || source.Target != nil {
			return errors.New("cc-notes source: create locators are invalid")
		}
	case catalog.MutationRevise, catalog.MutationDelete:
		if source.Object == nil || source.Parent == nil || source.Target != nil {
			return errors.New("cc-notes source: object locators are invalid")
		}
	case catalog.MutationReplace:
		if source.Object == nil || source.Parent == nil || source.Target == nil {
			return errors.New("cc-notes source: replace locators are invalid")
		}
	default:
		return errors.New("cc-notes source: mutation kind is invalid")
	}
	return nil
}

func mutationKind(source catalog.SourceMutationContext) (model.Kind, error) {
	if source.Operation.Kind == catalog.MutationCreate {
		if source.Parent == nil {
			return "", errors.New("cc-notes source: create parent is missing")
		}
		value, ok := strings.CutPrefix(string(source.Parent.SourceKey), "kind:")
		kind := model.Kind(value)
		layout, registered := layouts[kind]
		if !ok || !registered || codecOf(kind).ReadOnly() || !strings.HasSuffix(source.Operation.Name, layout.ext) {
			return "", errors.New("cc-notes source: create parent or filename is not writable")
		}
		return kind, nil
	}
	kind, _, err := entityLocator(source.Object)
	return kind, err
}

func entityLocator(locator *catalog.SourceLocator) (model.Kind, string, error) {
	if locator == nil {
		return "", "", errors.New("cc-notes source: entity locator is missing")
	}
	parts := strings.Split(string(locator.SourceKey), ":")
	if len(parts) != 3 || parts[0] != "entity" {
		return "", "", errors.New("cc-notes source: entity locator is malformed")
	}
	kind := model.Kind(parts[1])
	codec, ok := codecs[kind]
	if !ok || codec.ReadOnly() || parts[2] == "" {
		return "", "", errors.New("cc-notes source: entity locator is not writable")
	}
	return kind, string(locator.SourceKey), nil
}

func entitySourceKeyForNonce(kind model.Kind, nonce string) string {
	return "entity:" + string(kind) + ":" + nonce
}

func readMutationContent(input io.Reader) ([]byte, error) {
	if input == nil {
		return nil, errors.New("cc-notes source: mutation content is missing")
	}
	content, err := io.ReadAll(input)
	if err != nil {
		return nil, fmt.Errorf("cc-notes source: read mutation content: %w", err)
	}
	if len(content) == 0 {
		return nil, errors.New("cc-notes source: mutation content is empty")
	}
	return content, nil
}

func sourceWorkerEnvironment(environment []string, session string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "CC_NOTES_SESSION_ID=") {
			result = append(result, entry)
		}
	}
	return append(result, "CC_NOTES_SESSION_ID="+session)
}

var _ tenant.SourceMutationPlanner = sourceMutationPlanner{}
