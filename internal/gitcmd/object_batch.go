package gitcmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
)

// ObjectInfo identifies one untransformed object in the selected repository.
type ObjectInfo struct {
	Hash plumbing.Hash
	Type plumbing.ObjectType
	Size int64
}

// ObjectBatch owns a local Git object reader and its streaming protocol.
type ObjectBatch struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	ctx      context.Context
	cancel   context.CancelFunc
	input    io.WriteCloser
	output   *bufio.Reader
	stderr   *os.File
	body     *objectBody
	closed   bool
	released bool
	done     chan struct{}
	waitErr  error
}

// ObjectBatch opens a local-only reader under the backend's repository authority.
func (g Git) ObjectBatch(ctx context.Context) (*ObjectBatch, error) {
	ctx, cancel := context.WithCancel(ctx)
	batch := &ObjectBatch{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	args := append([]string{"--no-replace-objects", "--no-optional-locks", "--no-lazy-fetch"}, g.argv("cat-file", "--batch-command")...)
	//nolint:gosec // G204: git and cat-file mode are fixed; repository paths use the selected backend argv.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = g.environ(append(os.Environ(), noLazyFetchEnv))
	stderr, err := unlinkedTemp()
	if err != nil {
		cancel()
		return nil, err
	}
	batch.stderr = stderr
	cmd.Stderr = stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, errors.Join(err, stderr.Close())
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, errors.Join(err, input.Close(), stderr.Close())
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, errors.Join(err, input.Close(), output.Close(), stderr.Close())
	}
	batch.cmd, batch.input, batch.output = cmd, input, bufio.NewReader(output)
	go func() {
		batch.waitErr = cmd.Wait()
		close(batch.done)
	}()
	return batch, nil
}

// Idle reports whether this live reader can accept another object request.
func (b *ObjectBatch) Idle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed && !b.released && b.body == nil
}

func (b *ObjectBatch) request(command string, hash plumbing.Hash) (_ ObjectInfo, resultErr error) {
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, plumbing.ErrObjectNotFound) {
			resultErr = errors.Join(resultErr, b.ctx.Err(), b.abort())
		}
	}()
	if !b.Idle() {
		return ObjectInfo{}, errors.New("object batch is not idle")
	}
	if err := b.ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	if _, err := io.WriteString(b.input, command+" "+hash.String()+"\n"); err != nil {
		return ObjectInfo{}, err
	}
	line, err := b.output.ReadSlice('\n')
	if err != nil {
		return ObjectInfo{}, err
	}
	fields := strings.Fields(string(line))
	if len(fields) == 2 && fields[0] == hash.String() && fields[1] == "missing" {
		diagnostic, err := b.diagnostic()
		if err != nil {
			return ObjectInfo{}, err
		}
		if diagnostic != "" {
			return ObjectInfo{}, errors.New(diagnostic)
		}
		return ObjectInfo{}, plumbing.ErrObjectNotFound
	}
	if len(fields) != 3 || fields[0] != hash.String() {
		return ObjectInfo{}, errors.New("invalid git object header")
	}
	typ, err := plumbing.ParseObjectType(fields[1])
	if err != nil || typ < plumbing.CommitObject || typ > plumbing.TagObject {
		return ObjectInfo{}, errors.New("invalid git object type")
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return ObjectInfo{}, errors.New("invalid git object size")
	}
	return ObjectInfo{Hash: hash, Type: typ, Size: size}, nil
}

// Info reads an object's identity and size without loading its body.
func (b *ObjectBatch) Info(hash plumbing.Hash) (ObjectInfo, error) {
	return b.request("info", hash)
}

// Contents streams exactly the object described by info.
func (b *ObjectBatch) Contents(info ObjectInfo) (io.ReadCloser, error) {
	actual, err := b.request("contents", info.Hash)
	if err != nil {
		return nil, err
	}
	if actual != info {
		return nil, errors.Join(errors.New("git object identity changed"), b.abort())
	}
	body := &objectBody{batch: b, remaining: info.Size, expected: info.Hash, hasher: plumbing.NewHasher(info.Type, info.Size)}
	b.mu.Lock()
	b.body = body
	b.mu.Unlock()
	return body, nil
}

// Release closes an idle child or transfers its ownership to the active stream.
func (b *ObjectBatch) Release() error {
	b.mu.Lock()
	if b.closed || b.released {
		b.mu.Unlock()
		return nil
	}
	b.released = true
	active := b.body != nil
	b.mu.Unlock()
	if active {
		return nil
	}
	return b.Close()
}

// Close releases and reaps this reader's child process.
func (b *ObjectBatch) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	if b.body != nil {
		return b.abortLocked()
	}
	b.closed = true
	defer b.cancel()
	closeErr := b.closeInput()
	<-b.done
	waitErr := b.waitErr
	diagnostic, diagnosticErr := b.diagnostic()
	closeErr = errors.Join(closeErr, diagnosticErr, b.stderr.Close())
	if waitErr != nil {
		return errors.Join(closeErr, b.ctx.Err(), fmt.Errorf("git cat-file: %w: %s", waitErr, diagnostic))
	}
	if diagnostic != "" {
		return errors.Join(closeErr, errors.New(diagnostic))
	}
	return closeErr
}

func (b *ObjectBatch) abort() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.abortLocked()
}

func (b *ObjectBatch) abortLocked() error {
	if b.closed {
		return nil
	}
	b.closed = true
	b.cancel()
	closeErr := b.closeInput()
	<-b.done
	return errors.Join(closeErr, b.stderr.Close())
}

func (b *ObjectBatch) closeInput() error {
	err := b.input.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func (b *ObjectBatch) diagnostic() (string, error) {
	var data [65536]byte
	n, err := b.stderr.ReadAt(data[:], 0)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return strings.TrimSpace(string(data[:n])), err
}

type objectBody struct {
	batch     *ObjectBatch
	remaining int64
	expected  plumbing.Hash
	hasher    plumbing.Hasher
	closed    bool
}

func (r *objectBody) Read(data []byte) (int, error) {
	r.batch.mu.Lock()
	closed := r.batch.closed
	r.batch.mu.Unlock()
	if r.closed || closed {
		return 0, os.ErrClosed
	}
	if len(data) == 0 {
		return 0, nil
	}
	if err := r.batch.ctx.Err(); err != nil {
		return 0, err
	}
	if r.remaining == 0 {
		return 0, r.finish()
	}
	if int64(len(data)) > r.remaining {
		data = data[:r.remaining]
	}
	n, err := r.batch.output.Read(data)
	r.remaining -= int64(n)
	_, _ = r.hasher.Write(data[:n])
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return n, errors.Join(err, r.batch.ctx.Err())
}

func (r *objectBody) finish() error {
	end, err := r.batch.output.ReadByte()
	if err != nil {
		return errors.Join(err, r.batch.ctx.Err(), r.batch.abort())
	}
	if end != '\n' {
		return errors.Join(errors.New("invalid git object terminator"), r.batch.abort())
	}
	if r.hasher.Sum() != r.expected {
		return errors.Join(errors.New("git object content hash mismatch"), r.batch.abort())
	}
	r.batch.mu.Lock()
	r.batch.body = nil
	released := r.batch.released
	r.batch.mu.Unlock()
	r.closed = true
	if released {
		if err := r.batch.Close(); err != nil {
			return err
		}
	}
	return io.EOF
}

func (r *objectBody) Close() error {
	if r.closed {
		return nil
	}
	r.batch.mu.Lock()
	closed := r.batch.closed
	r.batch.mu.Unlock()
	if closed {
		r.closed = true
		return nil
	}
	if r.remaining == 0 {
		err := r.finish()
		if !errors.Is(err, io.EOF) {
			return errors.Join(err, r.batch.abort())
		}
		return nil
	}
	r.closed = true
	return r.batch.abort()
}
