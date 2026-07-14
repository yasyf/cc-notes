package mcpserver

import (
	"errors"
	"fmt"
)

// errStdinDash rejects a "-" free-text value: the CLI reads it from stdin, which
// the bridge pins empty, so a literal "-" over MCP would silently write "".
var errStdinDash = errors.New(`the literal "-" is reserved for the CLI's stdin form and is not supported via MCP; pass the text directly`)

// freeTextFlag appends flag value like optStr, but rejects a value of exactly
// "-" via errStdinDash. An empty value omits the flag.
func freeTextFlag(argv []string, flag, value string) ([]string, error) {
	switch value {
	case "":
		return argv, nil
	case "-":
		return nil, fmt.Errorf("%s: %w", flag, errStdinDash)
	default:
		return append(argv, flag, value), nil
	}
}
