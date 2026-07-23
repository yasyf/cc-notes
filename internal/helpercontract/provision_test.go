package helpercontract

import (
	"slices"
	"testing"

	"github.com/yasyf/fusekit/transportproto"

	"github.com/yasyf/cc-notes/internal/version"
)

func TestProvisionInvocationRequiresExactBuildAndProtocol(t *testing.T) {
	exact := ProvisionArguments("/repo")
	if want := []string{provisionOperation, version.String(), transportproto.WireBuild, "/repo"}; !slices.Equal(exact, want) {
		t.Fatalf("ProvisionArguments = %q, want %q", exact, want)
	}
	for _, test := range []struct {
		name     string
		args     []string
		build    string
		protocol string
		wantRoot string
		wantErr  bool
	}{
		{name: "exact", args: exact, build: version.String(), protocol: transportproto.WireBuild, wantRoot: "/repo"},
		{name: "new cli old helper build", args: exact, build: "old-helper", protocol: transportproto.WireBuild, wantErr: true},
		{name: "new cli old helper protocol", args: exact, build: version.String(), protocol: "old-protocol", wantErr: true},
		{name: "old cli new helper", args: []string{provisionOperation, "/repo"}, build: version.String(), protocol: transportproto.WireBuild, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, recognized, err := parseProvision(test.args, test.build, test.protocol)
			if !recognized || (err != nil) != test.wantErr || root != test.wantRoot {
				t.Fatalf("parseProvision = (%q, %v, %v), want (%q, true, error=%v)", root, recognized, err, test.wantRoot, test.wantErr)
			}
		})
	}
}

func TestParseProvisionIgnoresOtherServiceOperations(t *testing.T) {
	if root, recognized, err := ParseProvision([]string{"--install-service"}); root != "" || recognized || err != nil {
		t.Fatalf("ParseProvision = (%q, %v, %v)", root, recognized, err)
	}
}
