package fusefs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

func entitySourceKey(kind model.Kind, root model.PackCommit) (string, error) {
	if len(root.Pack.Ops) == 0 {
		return "", errors.New("root pack has no operations")
	}
	nonce, err := createNonce(root.Pack.Ops[0])
	if err != nil {
		return "", err
	}
	if nonce == "" || strings.ContainsAny(nonce, "/\\\x00") {
		return "", errors.New("create nonce is not an opaque source key component")
	}
	return "entity:" + string(kind) + ":" + nonce, nil
}

func createNonce(operation model.Op) (string, error) {
	switch value := operation.(type) {
	case model.CreateNote:
		return value.Nonce, nil
	case model.CreateDoc:
		return value.Nonce, nil
	case model.CreateLog:
		return value.Nonce, nil
	case model.CreateTask:
		return value.Nonce, nil
	case model.CreateSprint:
		return value.Nonce, nil
	case model.CreateProject:
		return value.Nonce, nil
	case model.CreateRunbook:
		return value.Nonce, nil
	case model.CreateInvestigation:
		return value.Nonce, nil
	default:
		return "", fmt.Errorf("operation %q is not a create", operation.OpKind())
	}
}

func setCreateNonce(operation model.Op, nonce string) (model.Op, error) {
	switch value := operation.(type) {
	case model.CreateNote:
		value.Nonce = nonce
		return value, nil
	case model.CreateDoc:
		value.Nonce = nonce
		return value, nil
	case model.CreateLog:
		value.Nonce = nonce
		return value, nil
	case model.CreateTask:
		value.Nonce = nonce
		return value, nil
	case model.CreateSprint:
		value.Nonce = nonce
		return value, nil
	case model.CreateProject:
		value.Nonce = nonce
		return value, nil
	case model.CreateRunbook:
		value.Nonce = nonce
		return value, nil
	case model.CreateInvestigation:
		value.Nonce = nonce
		return value, nil
	default:
		return nil, fmt.Errorf("operation %q is not a create", operation.OpKind())
	}
}
