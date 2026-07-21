package fusefs

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/catalogservice"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountservice"
)

const desiredFleetCASLimit = 8

// ProvisionRepository publishes its exact source declaration and durably provisions its tenant.
func ProvisionRepository(ctx context.Context, plan holder.RuntimePlan, repoRoot string) (resultErr error) {
	provision, err := NewRepositoryProvision(plan.Paths().PresentationRoot, repoRoot)
	if err != nil {
		return err
	}
	if err := publishRepositoryDeclaration(ctx, plan, provision.Declaration); err != nil {
		return err
	}
	client, err := mountservice.NewClient(ctx, wire.ClientConfig{Dial: wire.UnixDialer(plan.Paths().Socket)})
	if err != nil {
		return fmt.Errorf("cc-notes provision: connect tenant service: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, client.Close()) }()
	if _, err := client.ProvisionTenant(ctx, provision.Tenant.ID, provision.Definition); err != nil {
		return fmt.Errorf("cc-notes provision: provision repository tenant: %w", err)
	}
	return nil
}

func publishRepositoryDeclaration(
	ctx context.Context,
	plan holder.RuntimePlan,
	declaration catalog.SourceAuthorityDeclaration,
) (resultErr error) {
	client, err := catalogservice.NewClient(ctx, wire.ClientConfig{Dial: wire.UnixDialer(plan.Paths().Socket)})
	if err != nil {
		return fmt.Errorf("cc-notes provision: connect source fleet service: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, client.Close()) }()
	protocolDeclaration := protocolSourceAuthorityDeclaration(declaration)
	for attempt := 0; attempt < desiredFleetCASLimit; attempt++ {
		generation, declarations, err := readDesiredSourceFleet(ctx, client)
		if err != nil {
			return err
		}
		declarations, changed, err := mergeDesiredDeclaration(declarations, protocolDeclaration)
		if err != nil {
			return err
		}
		expectedGeneration := generation
		nextGeneration := generation + 1
		if !changed {
			if generation == 0 {
				return errors.New("cc-notes provision: empty desired source fleet reported an existing declaration")
			}
			expectedGeneration = generation - 1
			nextGeneration = generation
		} else if generation == math.MaxUint64 {
			return errors.New("cc-notes provision: desired source fleet is at its v1 bound")
		}
		response, err := client.PublishDesiredSourceFleet(ctx, catalogproto.PublishDesiredSourceFleetRequest{
			Protocol: catalogproto.Version, Owner: string(holderOwner),
			ExpectedGeneration: expectedGeneration, Generation: nextGeneration, Declarations: declarations,
		})
		if err == nil {
			if response.State == nil || response.State.Owner != string(holderOwner) ||
				response.State.Generation != nextGeneration || response.State.AuthorityCount != uint64(len(declarations)) {
				return errors.New("cc-notes provision: desired source fleet publication returned a mismatched state")
			}
			if err := validateProtocolFleetState(*response.State, declarations); err != nil {
				return err
			}
			return nil
		}
		var remote *catalogservice.RemoteError
		if !errors.As(err, &remote) || remote.Code != catalogproto.ErrorCodeConflict {
			return fmt.Errorf("cc-notes provision: publish desired source fleet: %w", err)
		}
	}
	return errors.New("cc-notes provision: desired source fleet changed during every bounded CAS attempt")
}

func mergeDesiredDeclaration(
	current []catalogproto.SourceAuthorityDeclaration,
	declaration catalogproto.SourceAuthorityDeclaration,
) ([]catalogproto.SourceAuthorityDeclaration, bool, error) {
	index, found := slices.BinarySearchFunc(
		current, declaration,
		func(left, right catalogproto.SourceAuthorityDeclaration) int {
			return bytes.Compare([]byte(left.Authority), []byte(right.Authority))
		},
	)
	if found {
		if sameProtocolSourceAuthorityDeclaration(current[index], declaration) {
			return current, false, nil
		}
		return nil, false, errors.New("cc-notes provision: repository source authority already has a different immutable declaration")
	}
	if len(current) >= int(catalogproto.MaxSourceFleetDeclarations) {
		return nil, false, errors.New("cc-notes provision: desired source fleet is at its v1 bound")
	}
	result := make([]catalogproto.SourceAuthorityDeclaration, len(current)+1)
	copy(result, current[:index])
	result[index] = declaration
	copy(result[index+1:], current[index:])
	return result, true, nil
}

func readDesiredSourceFleet(
	ctx context.Context,
	client *catalogservice.Client,
) (uint64, []catalogproto.SourceAuthorityDeclaration, error) {
	request := catalogproto.ReadDesiredSourceFleetRequest{
		Protocol: catalogproto.Version, Owner: string(holderOwner), Limit: catalogproto.MaxSourceFleetDeclarations,
	}
	response, err := client.ReadDesiredSourceFleet(ctx, request)
	if err != nil {
		var remote *catalogservice.RemoteError
		if errors.As(err, &remote) && remote.Code == catalogproto.ErrorCodeNotFound {
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("cc-notes provision: read desired source fleet: %w", err)
	}
	if response.State == nil || response.State.Owner != string(holderOwner) || response.State.Generation == 0 {
		return 0, nil, errors.New("cc-notes provision: desired source fleet head is invalid")
	}
	state := *response.State
	declarations := append([]catalogproto.SourceAuthorityDeclaration(nil), response.Declarations...)
	for response.Next != nil {
		snapshotDigest := state.DeclarationsDigest
		after := *response.Next
		response, err = client.ReadDesiredSourceFleet(ctx, catalogproto.ReadDesiredSourceFleetRequest{
			Protocol: catalogproto.Version, Owner: string(holderOwner), Generation: state.Generation,
			SnapshotDigest: &snapshotDigest, After: &after, Limit: catalogproto.MaxSourceFleetDeclarations,
		})
		if err != nil {
			return 0, nil, fmt.Errorf("cc-notes provision: continue desired source fleet snapshot: %w", err)
		}
		if response.State == nil || *response.State != state {
			return 0, nil, errors.New("cc-notes provision: desired source fleet snapshot fence changed")
		}
		if len(response.Declarations) == 0 ||
			(len(declarations) != 0 && response.Declarations[0].Authority <= declarations[len(declarations)-1].Authority) {
			return 0, nil, errors.New("cc-notes provision: desired source fleet pages are not exact and ordered")
		}
		declarations = append(declarations, response.Declarations...)
		if len(declarations) > int(catalogproto.MaxSourceFleetDeclarations) {
			return 0, nil, errors.New("cc-notes provision: desired source fleet exceeds its v1 bound")
		}
	}
	if uint64(len(declarations)) != state.AuthorityCount {
		return 0, nil, errors.New("cc-notes provision: desired source fleet count differs from its pinned state")
	}
	if err := validateProtocolFleetState(state, declarations); err != nil {
		return 0, nil, err
	}
	return state.Generation, declarations, nil
}

func validateProtocolFleetState(
	state catalogproto.DesiredSourceFleetState,
	declarations []catalogproto.SourceAuthorityDeclaration,
) error {
	catalogDeclarations := make([]catalog.SourceAuthorityDeclaration, len(declarations))
	authorities := make([]causal.SourceAuthorityID, len(declarations))
	for index, declaration := range declarations {
		digest, err := hex.DecodeString(declaration.DeclarationDigest)
		if err != nil || len(digest) != 32 {
			return errors.New("cc-notes provision: desired source fleet carries an invalid declaration digest")
		}
		authority := causal.SourceAuthorityID(declaration.Authority)
		authorities[index] = authority
		catalogDeclarations[index] = catalog.SourceAuthorityDeclaration{
			Authority: authority, DriverID: declaration.DriverID,
			DriverConfig: append([]byte(nil), declaration.DriverConfig...),
		}
		copy(catalogDeclarations[index].DeclarationDigest[:], digest)
	}
	authoritiesDigest, err := catalog.SourceAuthorityFleetDigest(authorities)
	if err != nil {
		return err
	}
	declarationsDigest, err := catalog.SourceAuthorityFleetDeclarationsDigest(catalogDeclarations)
	if err != nil {
		return err
	}
	if state.AuthoritiesDigest != hex.EncodeToString(authoritiesDigest[:]) ||
		state.DeclarationsDigest != hex.EncodeToString(declarationsDigest[:]) {
		return errors.New("cc-notes provision: desired source fleet digest differs from its exact declarations")
	}
	return nil
}

func protocolSourceAuthorityDeclaration(declaration catalog.SourceAuthorityDeclaration) catalogproto.SourceAuthorityDeclaration {
	return catalogproto.SourceAuthorityDeclaration{
		Authority: catalogproto.SourceAuthorityID(declaration.Authority), DriverID: declaration.DriverID,
		DriverConfig:      append([]byte(nil), declaration.DriverConfig...),
		DeclarationDigest: hex.EncodeToString(declaration.DeclarationDigest[:]),
	}
}

func sameProtocolSourceAuthorityDeclaration(left, right catalogproto.SourceAuthorityDeclaration) bool {
	return left.Authority == right.Authority && left.DriverID == right.DriverID &&
		bytes.Equal(left.DriverConfig, right.DriverConfig) && left.DeclarationDigest == right.DeclarationDigest
}
