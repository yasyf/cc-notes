package fusefs

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/cc-notes/model"
)

func entitySourceKey(kind model.Kind, root model.PackCommit) (string, error) {
	if !plumbing.IsHash(string(root.SHA)) {
		return "", fmt.Errorf("root commit %q has invalid source identity", root.SHA)
	}
	return "entity:" + string(kind) + ":" + string(root.SHA), nil
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
