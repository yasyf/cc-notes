//go:build darwin

package fusefs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/transportproto"
)

const (
	provisionCallTimeout = 2 * time.Minute
	businessCloseTimeout = 10 * time.Second
)

// BusinessHandlers returns cc-notes' complete product operation set.
func BusinessHandlers(plan holder.RuntimePlan) []holder.BusinessHandlerSpec {
	return []holder.BusinessHandlerSpec{{
		Op: helpercontract.ProvisionRepositoryOperation,
		Handler: func(
			ctx context.Context,
			request daemonkit.Request,
			controller *holder.LocalTenantController,
		) (any, error) {
			if request.Session == (daemonkit.Session{}) || request.Caller.UID != uint32(os.Getuid()) { //nolint:gosec // kernel UIDs are non-negative
				return nil, errors.New("cc-notes helper: repository provision peer is not exact")
			}
			var payload helpercontract.ProvisionRepositoryRequest
			if err := decodeBusinessPayload(request.Body, &payload); err != nil {
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
	client, err := daemonkit.Open(helperDaemon(plan))
	if err != nil {
		return fmt.Errorf("cc-notes helper: connect business session: %w", err)
	}
	business := client.Business()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), businessCloseTimeout)
		defer cancel()
		resultErr = errors.Join(resultErr, business.Close(closeCtx))
	}()
	return callProvisionRepository(ctx, business, expected)
}

func helperDaemon(plan holder.RuntimePlan) daemonkit.Daemon {
	return daemonkit.Daemon{
		Label:   daemonkit.Label(plan.Deployment().Agent().Label),
		Schemas: []daemonkit.Schema{daemonkit.Schema(transportproto.WireBuild)},
		Trust:   daemonkit.Trust{Serving: daemonkit.ServingSigned(plan.RuntimeRequirement())},
	}
}

type businessCaller interface {
	Call(ctx context.Context, op string, body []byte) (daemonkit.Reply, error)
}

func callProvisionRepository(
	ctx context.Context,
	client businessCaller,
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
	if _, stated := ctx.Deadline(); !stated {
		bounded, cancel := context.WithTimeout(ctx, provisionCallTimeout)
		defer cancel()
		ctx = bounded
	}
	reply, err := client.Call(ctx, helpercontract.ProvisionRepositoryOperation, payload)
	if err != nil {
		return fmt.Errorf("cc-notes helper: repository provision: %w", err)
	}
	var response helpercontract.ProvisionRepositoryResponse
	if err := decodeBusinessPayload(reply.Body, &response); err != nil {
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
