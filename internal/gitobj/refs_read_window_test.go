package gitobj

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestPackedReferenceReadWindowBytes(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 200)
	for _, off := range []int64{-1, 0, 500, 1023, 1024, 2800, 3199, 3200, 3201} {
		for _, size := range []int{0, 1, 32, 512, 1024, 4096} {
			t.Run(fmt.Sprintf("off=%d/size=%d", off, size), func(t *testing.T) {
				window := &packedRefReadWindow{reader: bytes.NewReader(data)}
				for range 2 {
					got, want := make([]byte, size), make([]byte, size)
					n, err := window.ReadAt(got, off)
					wantN, wantErr := bytes.NewReader(data).ReadAt(want, off)
					if n != wantN || fmt.Sprint(err) != fmt.Sprint(wantErr) || !bytes.Equal(got, want) {
						t.Fatalf("ReadAt = %d, %v, %x; want %d, %v, %x", n, err, got, wantN, wantErr, want)
					}
				}
			})
		}
	}
}

type packedRefReaderFunc func([]byte, int64) (int, error)

func (f packedRefReaderFunc) ReadAt(data []byte, off int64) (int, error) {
	return f(data, off)
}

func TestPackedReferenceReadWindowErrors(t *testing.T) {
	failure := errors.New("packed ref read failed")
	for _, tc := range []struct {
		name    string
		data    string
		err     error
		off     int64
		size    int
		want    string
		wantErr error
	}{
		{name: "partial EOF", data: "abc", err: io.EOF, size: 5, want: "abc", wantErr: io.EOF},
		{name: "exact EOF", data: "abc", err: io.EOF, size: 3, want: "abc"},
		{name: "before error", data: "abc", err: failure, size: 2, want: "ab"},
		{name: "partial error", data: "abc", err: failure, size: 5, want: "abc", wantErr: failure},
		{name: "exact error", data: "abc", err: failure, size: 3, want: "abc", wantErr: failure},
		{name: "offset error", data: "abc", err: failure, off: 1, size: 5, want: "bc", wantErr: failure},
		{name: "after error", data: "abc", err: failure, off: 4, size: 2, wantErr: failure},
		{name: "empty error", err: failure, size: 3, wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			window := &packedRefReadWindow{reader: packedRefReaderFunc(func(data []byte, off int64) (int, error) {
				calls++
				if off != 0 || len(data) != packedRefPageSize {
					t.Fatalf("page read = %d bytes at %d", len(data), off)
				}
				return copy(data, tc.data), tc.err
			})}
			for range 2 {
				data := make([]byte, tc.size)
				n, err := window.ReadAt(data, tc.off)
				if n != len(tc.want) || string(data[:n]) != tc.want || !errors.Is(err, tc.wantErr) {
					t.Fatalf("ReadAt = %q, %v; want %q, %v", data[:n], err, tc.want, tc.wantErr)
				}
			}
			if calls != 1 {
				t.Fatalf("source calls = %d; want 1", calls)
			}
		})
	}
}

func TestPackedReferenceReadWindowCrossPageError(t *testing.T) {
	failure := errors.New("packed ref read failed")
	data := bytes.Repeat([]byte("x"), packedRefPageSize+3)
	calls := 0
	window := &packedRefReadWindow{reader: packedRefReaderFunc(func(out []byte, off int64) (int, error) {
		calls++
		n := copy(out, data[off:])
		if n < len(out) {
			return n, failure
		}
		return n, nil
	})}
	for range 2 {
		out := make([]byte, 8)
		n, err := window.ReadAt(out, packedRefPageSize-2)
		if n != 5 || string(out[:n]) != "xxxxx" || !errors.Is(err, failure) {
			t.Fatalf("cross-page read = %q, %v; want xxxxx, %v", out[:n], err, failure)
		}
	}
	if calls != 2 {
		t.Fatalf("cross-page source calls = %d; want 2", calls)
	}
	out := make([]byte, 4096)
	n, err := window.ReadAt(out, 0)
	if n != len(data) || !bytes.Equal(out[:n], data) || !errors.Is(err, failure) || calls != 3 {
		t.Fatalf("streaming partial read = %d, %v, calls=%d; want %d, %v, 3", n, err, calls, len(data), failure)
	}
}

func TestPackedReferenceReadWindowBounded(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 1280)
	source := &countedReferenceReader{Reader: bytes.NewReader(data)}
	window := &packedRefReadWindow{reader: source}
	read := func(off int64, size int) {
		t.Helper()
		got := make([]byte, size)
		n, err := window.ReadAt(got, off)
		if err != nil || n != size || !bytes.Equal(got, data[off:off+int64(size)]) {
			t.Fatalf("ReadAt(%d, %d) = %d, %v", off, size, n, err)
		}
	}
	read(100, 512)
	read(500, 512)
	if source.calls != 1 {
		t.Fatalf("overlapping source calls = %d; want 1", source.calls)
	}
	for i := 1; i < 20; i++ {
		read(int64(i*packedRefPageSize), 512)
	}
	if window.count != len(window.pages) || source.calls != 20 {
		t.Fatalf("cached pages = %d, source calls = %d; want 8, 20", window.count, source.calls)
	}
	read(100, 512)
	if source.calls != 21 {
		t.Fatalf("evicted page source calls = %d; want 21", source.calls)
	}
	beforeCalls, beforeBytes := source.calls, source.read
	read(0, 4096)
	if source.calls-beforeCalls != 1 || source.read-beforeBytes != 4096 {
		t.Fatalf("streaming source calls = %d, bytes = %d; want 1, 4096", source.calls-beforeCalls, source.read-beforeBytes)
	}
	t.Logf("pages=%d page_bytes=%d retained_data_bytes=%d source_calls=%d source_bytes=%d", len(window.pages), packedRefPageSize, len(window.pages)*packedRefPageSize, source.calls, source.read)
}

func TestPackedReferenceReadWindowEquivalence(t *testing.T) {
	for _, count := range []int{32, 10000} {
		for _, sorted := range []bool{false, true} {
			t.Run(fmt.Sprintf("refs=%d/sorted=%t", count, sorted), func(t *testing.T) {
				var fixture strings.Builder
				fixture.WriteString("# pack-refs with: peeled fully-peeled")
				if sorted {
					fixture.WriteString(" sorted")
				}
				fixture.WriteByte('\n')
				for i := range count {
					index := i
					if !sorted {
						index = count - i - 1
					}
					fmt.Fprintf(&fixture, "%s refs/heads/branch-%05d\n", testRefHash, index)
					if index%3 == 0 {
						fmt.Fprintf(&fixture, "^%s\n", testRefHash)
					}
				}
				data := []byte(fixture.String())
				cachedCalls, directCalls := 0, 0
				for _, query := range []struct {
					value  string
					prefix bool
				}{
					{value: "refs/heads/branch-00000"},
					{value: fmt.Sprintf("refs/heads/branch-%05d", count/2)},
					{value: fmt.Sprintf("refs/heads/branch-%05d", count-1)},
					{value: fmt.Sprintf("refs/heads/branch-%05d", count)},
					{value: "refs/aaa"},
					{value: "refs/zzz"},
					{value: "refs/heads/branch-000", prefix: true},
					{value: "refs/heads/branch-05", prefix: true},
					{value: "refs/zzz", prefix: true},
					{value: "refs/heads/", prefix: true},
				} {
					var results [2]map[string]*plumbing.Reference
					var errs [2]error
					var calls, reads [2]int
					for mode := range 2 {
						source := &countedReferenceReader{Reader: bytes.NewReader(data)}
						packed, err := openPackedReferences(source, int64(len(data)))
						if err != nil {
							t.Fatal(err)
						}
						if mode == 1 {
							packed.reader = source
						}
						if query.prefix {
							results[mode], errs[mode] = packed.prefix(t.Context(), query.value)
						} else {
							var ref *plumbing.Reference
							ref, errs[mode] = packed.find(t.Context(), query.value)
							results[mode] = map[string]*plumbing.Reference{query.value: ref}
						}
						calls[mode], reads[mode] = source.calls, source.read
					}
					if !reflect.DeepEqual(results[0], results[1]) || !errors.Is(errs[0], errs[1]) {
						t.Fatalf("query %q prefix=%t: cached=%v, %v; direct=%v, %v", query.value, query.prefix, results[0], errs[0], results[1], errs[1])
					}
					want := make(map[string]*plumbing.Reference)
					for i := range count {
						name := fmt.Sprintf("refs/heads/branch-%05d", i)
						if name == query.value || query.prefix && strings.HasPrefix(name, query.value) {
							want[name] = plumbing.NewHashReference(plumbing.ReferenceName(name), plumbing.NewHash(testRefHash))
						}
					}
					if !query.prefix && len(want) == 0 {
						want[query.value] = nil
						if !errors.Is(errs[0], plumbing.ErrReferenceNotFound) {
							t.Fatalf("missing query %q: %v", query.value, errs[0])
						}
					} else if errs[0] != nil {
						t.Fatal(errs[0])
					}
					if !reflect.DeepEqual(results[0], want) {
						t.Fatalf("query %q prefix=%t: got %d refs, want %d", query.value, query.prefix, len(results[0]), len(want))
					}
					cachedCalls += calls[0]
					directCalls += calls[1]
					t.Logf("refs=%d sorted=%t query=%q prefix=%t cached_calls=%d direct_calls=%d cached_bytes=%d direct_bytes=%d", count, sorted, query.value, query.prefix, calls[0], calls[1], reads[0], reads[1])
					if sorted && !query.prefix && calls[0] >= calls[1] {
						t.Fatalf("query %q source calls: cached=%d direct=%d; want a reduction", query.value, calls[0], calls[1])
					}
				}
				if sorted && cachedCalls >= directCalls {
					t.Fatalf("source calls: cached=%d direct=%d; want a reduction", cachedCalls, directCalls)
				}
				t.Logf("refs=%d sorted=%t total_cached_calls=%d total_direct_calls=%d retained_data_bytes=%d", count, sorted, cachedCalls, directCalls, 8*packedRefPageSize)
			})
		}
	}
}
