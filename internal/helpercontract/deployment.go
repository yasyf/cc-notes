package helpercontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	deploymentOperation      = "--deployment-v1"
	deploymentResultIdentity = "cc-notes.helper.deployment-result.v1"
)

// DeploymentAction names one complete signed-helper deployment operation.
type DeploymentAction string

const (
	// DeploymentActivate reconciles the exact installed helper and service activation.
	DeploymentActivate DeploymentAction = "activate"
	// DeploymentDeactivate deactivates the service while retaining the signed helper.
	DeploymentDeactivate DeploymentAction = "deactivate"
)

// DeploymentState is the finite result of one complete deployment operation.
type DeploymentState string

const (
	// DeploymentActive means the exact helper generation and service plan are active.
	DeploymentActive DeploymentState = "active"
	// DeploymentInactive means the exact helper generation is retained without an active service.
	DeploymentInactive DeploymentState = "inactive"
	// DeploymentAbsent means no managed helper deployment existed.
	DeploymentAbsent DeploymentState = "absent"
)

// DeploymentRequest is the sole unsigned-CLI-to-signed-helper deployment request.
type DeploymentRequest struct {
	Action DeploymentAction
}

// DeploymentArguments returns the exact v1 signed-helper deployment invocation.
func DeploymentArguments(action DeploymentAction) []string {
	return []string{deploymentOperation, string(action)}
}

// ParseDeployment recognizes one exact v1 deployment business request.
func ParseDeployment(arguments []string) (DeploymentRequest, bool, error) {
	if len(arguments) == 0 || arguments[0] != deploymentOperation {
		return DeploymentRequest{}, false, nil
	}
	if len(arguments) != 2 {
		return DeploymentRequest{}, true, errors.New("cc-notes helper: deployment invocation has the wrong v1 shape")
	}
	action := DeploymentAction(arguments[1])
	if action != DeploymentActivate && action != DeploymentDeactivate {
		return DeploymentRequest{}, true, errors.New("cc-notes helper: deployment action is invalid")
	}
	return DeploymentRequest{Action: action}, true, nil
}

// DeploymentResult is the exact v1 outcome returned by the signed helper.
type DeploymentResult struct {
	Identity string           `json:"identity"`
	Schema   uint16           `json:"schema"`
	Action   DeploymentAction `json:"action"`
	State    DeploymentState  `json:"state"`
}

// NewDeploymentResult constructs one validated v1 deployment result.
func NewDeploymentResult(action DeploymentAction, state DeploymentState) (DeploymentResult, error) {
	result := DeploymentResult{
		Identity: deploymentResultIdentity, Schema: 1, Action: action, State: state,
	}
	if err := result.Validate(); err != nil {
		return DeploymentResult{}, err
	}
	return result, nil
}

// Validate requires the sole action-to-state mapping accepted by v1.
func (r DeploymentResult) Validate() error {
	if r.Identity != deploymentResultIdentity || r.Schema != 1 {
		return errors.New("cc-notes helper: deployment result identity is invalid")
	}
	switch r.Action {
	case DeploymentActivate:
		if r.State != DeploymentActive {
			return errors.New("cc-notes helper: activation result is not active")
		}
	case DeploymentDeactivate:
		if r.State != DeploymentInactive && r.State != DeploymentAbsent {
			return errors.New("cc-notes helper: deactivation result is invalid")
		}
	default:
		return errors.New("cc-notes helper: deployment result action is invalid")
	}
	return nil
}

// EncodeDeploymentResult returns canonical v1 JSON.
func EncodeDeploymentResult(result DeploymentResult) ([]byte, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("cc-notes helper: encode deployment result: %w", err)
	}
	return payload, nil
}

// DecodeDeploymentResult strictly decodes one canonical v1 result.
func DecodeDeploymentResult(payload []byte) (DeploymentResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var result DeploymentResult
	if err := decoder.Decode(&result); err != nil {
		return DeploymentResult{}, fmt.Errorf("cc-notes helper: decode deployment result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return DeploymentResult{}, errors.New("cc-notes helper: trailing deployment result")
		}
		return DeploymentResult{}, fmt.Errorf("cc-notes helper: decode deployment result tail: %w", err)
	}
	if err := result.Validate(); err != nil {
		return DeploymentResult{}, err
	}
	canonical, err := json.Marshal(result)
	if err != nil || !bytes.Equal(payload, canonical) {
		return DeploymentResult{}, errors.New("cc-notes helper: deployment result is not canonical v1 JSON")
	}
	return result, nil
}
