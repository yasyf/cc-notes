package helpercontract

import (
	"strings"
	"testing"
)

func TestDeploymentInvocationIsExact(t *testing.T) {
	for _, action := range []DeploymentAction{DeploymentActivate, DeploymentDeactivate} {
		request, recognized, err := ParseDeployment(DeploymentArguments(action))
		if err != nil || !recognized || request.Action != action {
			t.Fatalf("ParseDeployment(%q) = (%#v, %v, %v)", action, request, recognized, err)
		}
	}
	for _, arguments := range [][]string{
		{"--other"},
		{deploymentOperation},
		{deploymentOperation, string(DeploymentActivate), "extra"},
		{deploymentOperation, "restart"},
	} {
		_, recognized, err := ParseDeployment(arguments)
		if arguments[0] == "--other" {
			if recognized || err != nil {
				t.Fatalf("ParseDeployment(%q) recognized unrelated invocation", arguments)
			}
			continue
		}
		if !recognized || err == nil {
			t.Fatalf("ParseDeployment(%q) = (%v, %v), want recognized error", arguments, recognized, err)
		}
	}
}

func TestDeploymentResultIsCanonicalAndActionBound(t *testing.T) {
	for _, test := range []struct {
		action DeploymentAction
		state  DeploymentState
	}{
		{DeploymentActivate, DeploymentActive},
		{DeploymentDeactivate, DeploymentInactive},
		{DeploymentDeactivate, DeploymentAbsent},
	} {
		result, err := NewDeploymentResult(test.action, test.state)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := EncodeDeploymentResult(result)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeDeploymentResult(payload)
		if err != nil || decoded != result {
			t.Fatalf("DecodeDeploymentResult() = (%#v, %v), want %#v", decoded, err, result)
		}
	}
	for _, result := range []DeploymentResult{
		{Identity: deploymentResultIdentity, Schema: 1, Action: DeploymentActivate, State: DeploymentInactive},
		{Identity: deploymentResultIdentity, Schema: 1, Action: DeploymentDeactivate, State: DeploymentActive},
	} {
		if err := result.Validate(); err == nil {
			t.Fatalf("Validate accepted %#v", result)
		}
	}
	valid, err := NewDeploymentResult(DeploymentActivate, DeploymentActive)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeDeploymentResult(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, malformed := range [][]byte{
		append(append([]byte(nil), payload...), '\n'),
		[]byte(strings.Replace(string(payload), `"state":"active"`, `"state":"inactive"`, 1)),
	} {
		if _, err := DecodeDeploymentResult(malformed); err == nil {
			t.Fatalf("DecodeDeploymentResult accepted %q", malformed)
		}
	}
}
