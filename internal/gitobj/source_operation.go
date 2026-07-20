package gitobj

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/model"
)

const (
	sourceOperationFile   = "source-operation"
	sourceOperationHeader = "cc-notes-source-operation-v1"
)

// SourceOperationProof is one exact, immutable mutation replay record.
type SourceOperationProof struct {
	OperationID   string
	Expected      model.SHA
	Committed     model.SHA
	Result        string
	RequestDigest [sha256.Size]byte
}

// WriteSourceOperationProof stores one immutable proof whose sole parent is
// the source revision atomically committed by the operation.
func (r *Repo) WriteSourceOperationProof(ctx context.Context, proof SourceOperationProof) (model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := encodeSourceOperationProof(proof)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	blob, err := r.writeBlob(data)
	if err != nil {
		return "", fmt.Errorf("write source operation blob: %w", err)
	}
	tree, err := r.writeObject(&object.Tree{Entries: []object.TreeEntry{{Name: sourceOperationFile, Mode: filemode.Regular, Hash: blob}}})
	if err != nil {
		return "", fmt.Errorf("write source operation tree: %w", err)
	}
	identity := object.Signature{
		Name: "CC Notes Source Driver", Email: "source-driver@cc-notes.invalid", When: time.Unix(0, 0).UTC(),
	}
	sha, err := r.writeObject(&object.Commit{
		Author: identity, Committer: identity, Message: "cc-notes: source operation",
		TreeHash: tree, ParentHashes: []plumbing.Hash{plumbing.NewHash(string(proof.Committed))},
	})
	if err != nil {
		return "", fmt.Errorf("write source operation commit: %w", err)
	}
	return model.SHA(sha.String()), nil
}

// ReadSourceOperationProof reads and strictly validates one operation proof.
func (r *Repo) ReadSourceOperationProof(ctx context.Context, sha model.SHA) (SourceOperationProof, error) {
	if err := ctx.Err(); err != nil {
		return SourceOperationProof{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	commit, err := r.commit(sha)
	if err != nil {
		return SourceOperationProof{}, fmt.Errorf("read source operation %s: %w", sha, err)
	}
	if len(commit.ParentHashes) != 1 {
		return SourceOperationProof{}, errors.New("source operation proof must have one committed-revision parent")
	}
	file, err := commit.File(sourceOperationFile)
	if err != nil {
		return SourceOperationProof{}, fmt.Errorf("read source operation file: %w", err)
	}
	reader, err := file.Reader()
	if err != nil {
		return SourceOperationProof{}, fmt.Errorf("read source operation body: %w", err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return SourceOperationProof{}, fmt.Errorf("read source operation body: %w", readErr)
	}
	if closeErr != nil {
		return SourceOperationProof{}, fmt.Errorf("close source operation body: %w", closeErr)
	}
	proof, err := decodeSourceOperationProof(data)
	if err != nil {
		return SourceOperationProof{}, err
	}
	if proof.Committed != model.SHA(commit.ParentHashes[0].String()) {
		return SourceOperationProof{}, errors.New("source operation committed revision differs from parent")
	}
	return proof, nil
}

func encodeSourceOperationProof(proof SourceOperationProof) ([]byte, error) {
	if err := validateSourceOperationProof(proof); err != nil {
		return nil, err
	}
	return []byte(strings.Join([]string{
		sourceOperationHeader,
		proof.OperationID,
		string(proof.Expected),
		string(proof.Committed),
		proof.Result,
		hex.EncodeToString(proof.RequestDigest[:]),
		"",
	}, "\n")), nil
}

func decodeSourceOperationProof(data []byte) (SourceOperationProof, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lines := make([]string, 0, 6)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return SourceOperationProof{}, fmt.Errorf("decode source operation: %w", err)
	}
	if len(lines) != 6 || lines[0] != sourceOperationHeader || len(data) == 0 || data[len(data)-1] != '\n' {
		return SourceOperationProof{}, errors.New("source operation proof has invalid framing")
	}
	digest, err := hex.DecodeString(lines[5])
	if err != nil || len(digest) != sha256.Size {
		return SourceOperationProof{}, errors.New("source operation proof has invalid request digest")
	}
	proof := SourceOperationProof{
		OperationID: lines[1], Expected: model.SHA(lines[2]), Committed: model.SHA(lines[3]), Result: lines[4],
	}
	copy(proof.RequestDigest[:], digest)
	if err := validateSourceOperationProof(proof); err != nil {
		return SourceOperationProof{}, err
	}
	return proof, nil
}

func validateSourceOperationProof(proof SourceOperationProof) error {
	switch {
	case len(proof.OperationID) != 64:
		return errors.New("source operation id must be 64 lowercase hex characters")
	case !plumbing.IsHash(string(proof.Expected)) || !plumbing.IsHash(string(proof.Committed)):
		return errors.New("source operation revisions are invalid")
	case strings.ContainsAny(proof.Result, "\x00\n\r"):
		return errors.New("source operation result is invalid")
	}
	for _, character := range proof.OperationID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return errors.New("source operation id must be 64 lowercase hex characters")
		}
	}
	return nil
}
