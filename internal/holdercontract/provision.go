// Package holdercontract defines cc-notes' exact CLI-to-holder invocation.
package holdercontract

import (
	"errors"
	"fmt"

	"github.com/yasyf/fusekit/transportproto"

	"github.com/yasyf/cc-notes/internal/version"
)

const provisionOperation = "--provision-repository"

// ProvisionArguments returns the exact hard-cut repository provisioning invocation.
func ProvisionArguments(repoRoot string) []string {
	return []string{provisionOperation, version.String(), transportproto.Build, repoRoot}
}

// ParseProvision recognizes and authenticates one exact provisioning invocation.
func ParseProvision(arguments []string) (string, bool, error) {
	return parseProvision(arguments, version.String(), transportproto.Build)
}

func parseProvision(arguments []string, holderBuild, holderProtocol string) (string, bool, error) {
	if len(arguments) == 0 || arguments[0] != provisionOperation {
		return "", false, nil
	}
	if len(arguments) != 4 {
		return "", true, errors.New("cc-notes holder: provisioning invocation has the wrong v1 shape")
	}
	if arguments[1] != holderBuild {
		return "", true, fmt.Errorf("cc-notes holder: caller build %q differs from holder build %q", arguments[1], holderBuild)
	}
	if arguments[2] != holderProtocol {
		return "", true, errors.New("cc-notes holder: caller protocol differs from holder protocol")
	}
	return arguments[3], true, nil
}
