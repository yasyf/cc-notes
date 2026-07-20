package gitobj

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
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
	sourceOperationAttrs  = "retained-lfs-* filter=lfs diff=lfs merge=lfs -text\n"
)

// git-lfs has no explicit content-pin ref. It classifies branch content by
// commit recency, so an Applied proof uses the largest timestamp Git's commit
// graph stores exactly; the atomic Ack transaction removes that branch.
var sourceOperationLFSRetentionTime = time.Unix((1<<34)-1, 0).UTC()

// SourceOperationState is the durable receipt lifecycle for one mutation.
type SourceOperationState uint8

const (
	// SourceOperationApplied retains the exact source and content view.
	SourceOperationApplied SourceOperationState = iota + 1
	// SourceOperationAcknowledged releases the retained content view.
	SourceOperationAcknowledged
	// SourceOperationForgotten preserves only the consumed operation identity.
	SourceOperationForgotten
)

// SourceOperationProof is one exact, immutable mutation replay record.
type SourceOperationProof struct {
	OperationID   string
	State         SourceOperationState
	Expected      model.SHA
	Committed     model.SHA
	Result        string
	RequestDigest [sha256.Size]byte
	ReceiptDigest [sha256.Size]byte
	RetainedTips  []model.SHA
	RetainedLFS   []SourceOperationLFS
}

// SourceOperationLFS is one exact LFS body pinned by an applied proof.
type SourceOperationLFS struct {
	OID  string
	Size int64
}

// WriteSourceOperationProof stores one immutable operation proof.
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
	entries := []object.TreeEntry{{Name: sourceOperationFile, Mode: filemode.Regular, Hash: blob}}
	if len(proof.RetainedLFS) > 0 {
		attrs, err := r.writeBlob([]byte(sourceOperationAttrs))
		if err != nil {
			return "", fmt.Errorf("write retained LFS attributes: %w", err)
		}
		entries = append(entries, object.TreeEntry{Name: ".gitattributes", Mode: filemode.Regular, Hash: attrs})
	}
	for _, retained := range proof.RetainedLFS {
		pointer, err := r.writeBlob(sourceOperationLFSPointer(retained))
		if err != nil {
			return "", fmt.Errorf("write retained LFS pointer %s: %w", retained.OID, err)
		}
		entries = append(entries, object.TreeEntry{Name: sourceOperationLFSFile(retained.OID), Mode: filemode.Regular, Hash: pointer})
	}
	slices.SortFunc(entries, func(left, right object.TreeEntry) int { return strings.Compare(left.Name, right.Name) })
	tree, err := r.writeObject(&object.Tree{Entries: entries})
	if err != nil {
		return "", fmt.Errorf("write source operation tree: %w", err)
	}
	when := time.Unix(0, 0).UTC()
	if len(proof.RetainedLFS) > 0 {
		when = sourceOperationLFSRetentionTime
	}
	identity := object.Signature{
		Name: "CC Notes Source Driver", Email: "source-driver@cc-notes.invalid", When: when,
	}
	parents := make([]plumbing.Hash, 0, 1+len(proof.RetainedTips))
	if proof.State != SourceOperationForgotten {
		parents = append(parents, plumbing.NewHash(string(proof.Committed)))
	}
	for _, tip := range proof.RetainedTips {
		parents = append(parents, plumbing.NewHash(string(tip)))
	}
	sha, err := r.writeObject(&object.Commit{
		Author: identity, Committer: identity, Message: "cc-notes: source operation",
		TreeHash: tree, ParentHashes: parents,
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
	tree, err := commit.Tree()
	if err != nil {
		return SourceOperationProof{}, fmt.Errorf("read source operation tree: %w", err)
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
	parents := commit.ParentHashes
	switch proof.State {
	case SourceOperationApplied:
		if len(parents) != 1+len(proof.RetainedTips) {
			return SourceOperationProof{}, errors.New("applied source operation proof has invalid parents")
		}
		if proof.Committed != model.SHA(parents[0].String()) {
			return SourceOperationProof{}, errors.New("source operation committed revision differs from parent")
		}
		for offset, retained := range proof.RetainedTips {
			if retained != model.SHA(parents[offset+1].String()) {
				return SourceOperationProof{}, errors.New("source operation retained tip differs from parent")
			}
		}
	case SourceOperationAcknowledged:
		if len(parents) != 1 || proof.Committed != model.SHA(parents[0].String()) {
			return SourceOperationProof{}, errors.New("acknowledged source operation proof has invalid committed parent")
		}
	case SourceOperationForgotten:
		if len(parents) != 0 {
			return SourceOperationProof{}, errors.New("forgotten source operation proof must be parentless")
		}
	}
	expectedFiles := make(map[string]string, 1+len(proof.RetainedLFS))
	expectedFiles[sourceOperationFile] = string(data)
	if len(proof.RetainedLFS) > 0 {
		expectedFiles[".gitattributes"] = sourceOperationAttrs
	}
	for _, retained := range proof.RetainedLFS {
		expectedFiles[sourceOperationLFSFile(retained.OID)] = string(sourceOperationLFSPointer(retained))
	}
	if len(tree.Entries) != len(expectedFiles) {
		return SourceOperationProof{}, errors.New("source operation proof tree has unexpected entries")
	}
	for _, entry := range tree.Entries {
		want, found := expectedFiles[entry.Name]
		if !found || entry.Mode != filemode.Regular {
			return SourceOperationProof{}, errors.New("source operation proof tree entry is invalid")
		}
		entryFile, err := commit.File(entry.Name)
		if err != nil {
			return SourceOperationProof{}, fmt.Errorf("read source operation tree entry %s: %w", entry.Name, err)
		}
		got, err := entryFile.Contents()
		if err != nil {
			return SourceOperationProof{}, fmt.Errorf("read source operation tree entry %s: %w", entry.Name, err)
		}
		if got != want {
			return SourceOperationProof{}, errors.New("source operation proof tree content differs")
		}
	}
	return proof, nil
}

func encodeSourceOperationProof(proof SourceOperationProof) ([]byte, error) {
	if err := validateSourceOperationProof(proof); err != nil {
		return nil, err
	}
	lines := []string{
		sourceOperationHeader,
		proof.OperationID,
		fmt.Sprintf("%d", proof.State),
		string(proof.Expected),
		string(proof.Committed),
		proof.Result,
		hex.EncodeToString(proof.RequestDigest[:]),
		hex.EncodeToString(proof.ReceiptDigest[:]),
		strconv.Itoa(len(proof.RetainedTips)),
	}
	for _, tip := range proof.RetainedTips {
		lines = append(lines, string(tip))
	}
	lines = append(lines, strconv.Itoa(len(proof.RetainedLFS)))
	for _, retained := range proof.RetainedLFS {
		lines = append(lines, retained.OID+" "+strconv.FormatInt(retained.Size, 10))
	}
	return []byte(strings.Join(append(lines, ""), "\n")), nil
}

func decodeSourceOperationProof(data []byte) (SourceOperationProof, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lines := make([]string, 0, 9)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return SourceOperationProof{}, fmt.Errorf("decode source operation: %w", err)
	}
	if len(lines) < 10 || lines[0] != sourceOperationHeader || len(data) == 0 || data[len(data)-1] != '\n' {
		return SourceOperationProof{}, errors.New("source operation proof has invalid framing")
	}
	requestDigest, err := hex.DecodeString(lines[6])
	if err != nil || len(requestDigest) != sha256.Size {
		return SourceOperationProof{}, errors.New("source operation proof has invalid request digest")
	}
	receiptDigest, err := hex.DecodeString(lines[7])
	if err != nil || len(receiptDigest) != sha256.Size {
		return SourceOperationProof{}, errors.New("source operation proof has invalid receipt digest")
	}
	retainedCount, err := strconv.Atoi(lines[8])
	if err != nil || retainedCount < 0 || strconv.Itoa(retainedCount) != lines[8] || len(lines) < 10+retainedCount {
		return SourceOperationProof{}, errors.New("source operation proof has invalid retained-tip count")
	}
	lfsCountOffset := 9 + retainedCount
	lfsCount, err := strconv.Atoi(lines[lfsCountOffset])
	if err != nil || lfsCount < 0 || strconv.Itoa(lfsCount) != lines[lfsCountOffset] || len(lines) != 10+retainedCount+lfsCount {
		return SourceOperationProof{}, errors.New("source operation proof has invalid retained-LFS count")
	}
	proof := SourceOperationProof{
		OperationID: lines[1], Expected: model.SHA(lines[3]), Committed: model.SHA(lines[4]), Result: lines[5],
	}
	if retainedCount > 0 {
		proof.RetainedTips = make([]model.SHA, retainedCount)
	}
	if lfsCount > 0 {
		proof.RetainedLFS = make([]SourceOperationLFS, lfsCount)
	}
	state, err := strconv.Atoi(lines[2])
	if err != nil || strconv.Itoa(state) != lines[2] || state < 1 || state > 255 {
		return SourceOperationProof{}, errors.New("source operation proof has invalid state")
	}
	proof.State = SourceOperationState(state)
	copy(proof.RequestDigest[:], requestDigest)
	copy(proof.ReceiptDigest[:], receiptDigest)
	for offset, tip := range lines[9:lfsCountOffset] {
		proof.RetainedTips[offset] = model.SHA(tip)
	}
	for offset, encoded := range lines[lfsCountOffset+1:] {
		oid, sizeText, found := strings.Cut(encoded, " ")
		size, sizeErr := strconv.ParseInt(sizeText, 10, 64)
		if !found || sizeErr != nil || strconv.FormatInt(size, 10) != sizeText {
			return SourceOperationProof{}, errors.New("source operation proof has invalid retained LFS body")
		}
		proof.RetainedLFS[offset] = SourceOperationLFS{OID: oid, Size: size}
	}
	if err := validateSourceOperationProof(proof); err != nil {
		return SourceOperationProof{}, err
	}
	return proof, nil
}

func validateSourceOperationProof(proof SourceOperationProof) error {
	switch {
	case len(proof.OperationID) != 64:
		return errors.New("source operation id must be 64 lowercase hex characters")
	case strings.ContainsAny(proof.Result, "\x00\n\r"):
		return errors.New("source operation result is invalid")
	case proof.State < SourceOperationApplied || proof.State > SourceOperationForgotten:
		return errors.New("source operation state is invalid")
	}
	for _, character := range proof.OperationID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return errors.New("source operation id must be 64 lowercase hex characters")
		}
	}
	zeroDigest := [sha256.Size]byte{}
	if proof.RequestDigest == zeroDigest {
		return errors.New("source operation request digest is empty")
	}
	switch proof.State {
	case SourceOperationApplied:
		if !plumbing.IsHash(string(proof.Expected)) || !plumbing.IsHash(string(proof.Committed)) {
			return errors.New("source operation revisions are invalid")
		}
		if proof.ReceiptDigest != zeroDigest {
			return errors.New("applied source operation receipt digest must be empty")
		}
	case SourceOperationAcknowledged:
		if !plumbing.IsHash(string(proof.Expected)) || !plumbing.IsHash(string(proof.Committed)) {
			return errors.New("source operation revisions are invalid")
		}
		if proof.ReceiptDigest == zeroDigest || len(proof.RetainedTips) != 0 || len(proof.RetainedLFS) != 0 {
			return errors.New("acknowledged source operation proof is invalid")
		}
	case SourceOperationForgotten:
		if proof.Expected != "" || proof.Committed != "" || proof.Result != "" || proof.ReceiptDigest == zeroDigest || len(proof.RetainedTips) != 0 || len(proof.RetainedLFS) != 0 {
			return errors.New("forgotten source operation proof is invalid")
		}
	}
	if !slices.IsSorted(proof.RetainedTips) {
		return errors.New("source operation retained tips are not canonical")
	}
	for offset, tip := range proof.RetainedTips {
		if !plumbing.IsHash(string(tip)) || tip == proof.Committed || offset > 0 && tip == proof.RetainedTips[offset-1] {
			return errors.New("source operation retained tips are invalid")
		}
	}
	for offset, retained := range proof.RetainedLFS {
		if !model.ValidAttachmentOID(retained.OID) || retained.Size <= 0 ||
			offset > 0 && retained.OID <= proof.RetainedLFS[offset-1].OID {
			return errors.New("source operation retained LFS bodies are invalid")
		}
	}
	return nil
}

func sourceOperationLFSFile(oid string) string {
	return "retained-lfs-" + oid
}

func sourceOperationLFSPointer(retained SourceOperationLFS) []byte {
	return []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + retained.OID + "\nsize " + strconv.FormatInt(retained.Size, 10) + "\n")
}
