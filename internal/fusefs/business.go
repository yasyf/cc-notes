package fusefs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/transportproto"
)

// BusinessHandlers returns cc-notes' complete product operation set.
func BusinessHandlers(plan holder.RuntimePlan) []holder.BusinessHandlerSpec {
	return []holder.BusinessHandlerSpec{{
		Op: helpercontract.ProvisionRepositoryOperation,
		Handler: func(
			ctx context.Context,
			request wire.Request,
			controller *holder.LocalTenantController,
		) (any, error) {
			if request.Tenant != "" || request.Session == nil || request.Peer.UID != os.Getuid() ||
				request.WireBuild != transportproto.WireBuild {
				return nil, errors.New("cc-notes helper: repository provision peer or route is not exact")
			}
			var payload helpercontract.ProvisionRepositoryRequest
			if err := decodeBusinessPayload(request.Payload, &payload); err != nil {
				return nil, fmt.Errorf("cc-notes helper: decode repository provision request: %w", err)
			}
			if err := payload.Validate(); err != nil {
				return nil, err
			}
			return ProvisionRepositoryLocal(ctx, controller, plan, payload.RepositoryRoot)
		},
	}}
}

// ProvisionRepository provisions one repository over the helper's existing
// persistent daemonkit session.
func ProvisionRepository(ctx context.Context, plan holder.RuntimePlan, repoRoot string) (resultErr error) {
	expected, err := NewRepositoryProvision(plan.Paths().PresentationRoot, repoRoot)
	if err != nil {
		return err
	}
	client, err := wire.NewClient(ctx, wire.ClientConfig{
		Dial: wire.UnixDialer(plan.Paths().Socket), WireBuild: transportproto.WireBuild,
	})
	if err != nil {
		return fmt.Errorf("cc-notes helper: connect business session: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, client.Close()) }()
	return callProvisionRepository(ctx, client, expected)
}

func callProvisionRepository(
	ctx context.Context,
	client wire.UnaryClient,
	expected RepositoryProvision,
) error {
	request := helpercontract.ProvisionRepositoryRequest{
		Schema: helpercontract.ProvisionSchema, RepositoryRoot: expected.Tenant.RepoRoot,
	}
	if err := request.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("cc-notes helper: encode repository provision request: %w", err)
	}
	result, err := client.Call(ctx, helpercontract.ProvisionRepositoryOperation, "", payload)
	if err != nil {
		return fmt.Errorf("cc-notes helper: repository provision %s: %w", result.Outcome, err)
	}
	if result.Outcome != wire.Delivered || !result.Response.Ack || result.Response.Rejected ||
		result.Response.Code != "" || result.Response.Reason != "" {
		return errors.New("cc-notes helper: repository provision did not return one exact delivered response")
	}
	if result.Response.Err != "" {
		return fmt.Errorf("cc-notes helper: repository provision failed: %s", result.Response.Err)
	}
	var response helpercontract.ProvisionRepositoryResponse
	if err := decodeBusinessPayload(result.Response.Payload, &response); err != nil {
		return fmt.Errorf("cc-notes helper: decode repository provision response: %w", err)
	}
	if err := response.Validate(); err != nil {
		return err
	}
	if response.Tenant != string(expected.Spec.ID) || response.Generation != uint64(expected.Spec.Generation) {
		return errors.New("cc-notes helper: repository provision response names a different tenant generation")
	}
	return nil
}

func decodeBusinessPayload(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("business payload contains multiple JSON values")
		}
		return err
	}
	return nil
}
