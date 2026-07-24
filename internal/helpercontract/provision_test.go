package helpercontract

import "testing"

func TestRepositoryProvisionBusinessContractIsExactV1(t *testing.T) {
	exact := ProvisionRepositoryRequest{Schema: ProvisionSchema, RepositoryRoot: "/repo"}
	if err := exact.Validate(); err != nil {
		t.Fatalf("exact request: %v", err)
	}
	for _, request := range []ProvisionRepositoryRequest{
		{Schema: 2, RepositoryRoot: "/repo"},
		{Schema: ProvisionSchema, RepositoryRoot: "repo"},
		{Schema: ProvisionSchema, RepositoryRoot: "/repo/../other"},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("inexact request accepted: %+v", request)
		}
	}
	if err := (ProvisionRepositoryResponse{Schema: ProvisionSchema, Tenant: "tenant", Generation: 1}).Validate(); err != nil {
		t.Fatalf("exact response: %v", err)
	}
	for _, response := range []ProvisionRepositoryResponse{
		{Schema: 2, Tenant: "tenant", Generation: 1},
		{Schema: ProvisionSchema, Generation: 1},
		{Schema: ProvisionSchema, Tenant: "tenant"},
	} {
		if err := response.Validate(); err == nil {
			t.Fatalf("inexact response accepted: %+v", response)
		}
	}
}
