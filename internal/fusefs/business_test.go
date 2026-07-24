package fusefs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit/wire"
)

type businessClientStub struct {
	result  wire.Result
	err     error
	op      wire.Op
	tenant  string
	payload []byte
}

func (c *businessClientStub) Call(
	_ context.Context,
	op wire.Op,
	tenant string,
	payload []byte,
) (wire.Result, error) {
	c.op, c.tenant, c.payload = op, tenant, append([]byte(nil), payload...)
	return c.result, c.err
}

func (*businessClientStub) WireBuild() string { return "test" }

func TestProvisionRepositoryBusinessCallRequiresExactDeliveredProof(t *testing.T) {
	expected, err := NewRepositoryProvision(
		filepath.Join(t.TempDir(), "mount"), filepath.Join(t.TempDir(), "repository"),
	)
	if err != nil {
		t.Fatal(err)
	}
	response := helpercontract.ProvisionRepositoryResponse{
		Schema: helpercontract.ProvisionSchema, Tenant: string(expected.Spec.ID),
		Generation: uint64(expected.Spec.Generation),
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	client := &businessClientStub{result: wire.Result{
		Outcome: wire.Delivered, Response: wire.Response{Ack: true, Payload: payload},
	}}
	if err := callProvisionRepository(t.Context(), client, expected); err != nil {
		t.Fatalf("exact business call: %v", err)
	}
	if client.op != helpercontract.ProvisionRepositoryOperation || client.tenant != "" {
		t.Fatalf("business route = (%q, %q)", client.op, client.tenant)
	}
	var request helpercontract.ProvisionRepositoryRequest
	if err := decodeBusinessPayload(client.payload, &request); err != nil ||
		request.RepositoryRoot != expected.Tenant.RepoRoot {
		t.Fatalf("business request = %+v, %v", request, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*businessClientStub)
	}{
		{name: "transport", mutate: func(client *businessClientStub) { client.err = context.DeadlineExceeded }},
		{name: "unknown delivery", mutate: func(client *businessClientStub) { client.result.Outcome = wire.DeliveryUnknown }},
		{name: "missing ack", mutate: func(client *businessClientStub) { client.result.Response.Ack = false }},
		{name: "remote error", mutate: func(client *businessClientStub) { client.result.Response.Err = "failed" }},
		{name: "wrong tenant", mutate: func(client *businessClientStub) {
			wrong := response
			wrong.Tenant = "other"
			client.result.Response.Payload, _ = json.Marshal(wrong)
		}},
		{name: "unknown response field", mutate: func(client *businessClientStub) {
			client.result.Response.Payload = []byte(`{"schema":1,"tenant":"x","generation":1,"extra":true}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := &businessClientStub{result: client.result}
			test.mutate(candidate)
			if err := callProvisionRepository(t.Context(), candidate, expected); err == nil {
				t.Fatal("inexact business response was accepted")
			}
		})
	}
}

func TestDecodeBusinessPayloadRejectsTrailingOrUnknownData(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte(`{"schema":1,"repository_root":"/repo","extra":true}`),
		[]byte(`{"schema":1,"repository_root":"/repo"} {}`),
	} {
		var request helpercontract.ProvisionRepositoryRequest
		if err := decodeBusinessPayload(payload, &request); err == nil {
			t.Fatalf("inexact payload accepted: %s", payload)
		}
	}
	var request helpercontract.ProvisionRepositoryRequest
	if err := decodeBusinessPayload([]byte(`{"schema":1,"repository_root":"/repo"}`), &request); err != nil {
		t.Fatalf("exact payload: %v", err)
	}
}
