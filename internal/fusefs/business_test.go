package fusefs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit"
)

type businessClientStub struct {
	reply daemonkit.Reply
	err   error
	op    string
	body  []byte
}

func (c *businessClientStub) Call(_ context.Context, op string, body []byte) (daemonkit.Reply, error) {
	c.op, c.body = op, append([]byte(nil), body...)
	return c.reply, c.err
}

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
	client := &businessClientStub{reply: daemonkit.Reply{Body: payload}}
	if err := callProvisionRepository(t.Context(), client, expected); err != nil {
		t.Fatalf("exact business call: %v", err)
	}
	if client.op != helpercontract.ProvisionRepositoryOperation {
		t.Fatalf("business route = %q", client.op)
	}
	var request helpercontract.ProvisionRepositoryRequest
	if err := decodeBusinessPayload(client.body, &request); err != nil ||
		request.RepositoryRoot != expected.Tenant.RepoRoot {
		t.Fatalf("business request = %+v, %v", request, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*businessClientStub)
	}{
		{name: "transport", mutate: func(client *businessClientStub) { client.err = context.DeadlineExceeded }},
		{name: "product error", mutate: func(client *businessClientStub) {
			client.err = &daemonkit.ProductError{Code: "provision", Message: "failed"}
		}},
		{name: "wrong tenant", mutate: func(client *businessClientStub) {
			wrong := response
			wrong.Tenant = "other"
			client.reply.Body, _ = json.Marshal(wrong)
		}},
		{name: "unknown response field", mutate: func(client *businessClientStub) {
			client.reply.Body = []byte(`{"schema":1,"tenant":"x","generation":1,"extra":true}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := &businessClientStub{reply: client.reply}
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
