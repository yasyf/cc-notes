package gitobj

import (
	"errors"
	"io"
)

const packedRefPageSize = 1024

type packedRefReadPage struct {
	data  [packedRefPageSize]byte
	start int64
	n     int
	err   error
}

type packedRefReadWindow struct {
	reader io.ReaderAt
	pages  [8]packedRefReadPage
	count  int
	next   int
}

func (w *packedRefReadWindow) ReadAt(data []byte, off int64) (int, error) {
	if len(data) == 0 || len(data) >= packedRefPageSize || off < 0 {
		return w.reader.ReadAt(data, off)
	}
	read := 0
	for read < len(data) {
		start := off - off%packedRefPageSize
		page := w.page(start)
		within := int(off - start)
		if within >= page.n {
			return read, page.err
		}
		n := copy(data[read:], page.data[within:page.n])
		read += n
		off += int64(n)
		if within+n == page.n && page.err != nil {
			if read < len(data) || !errors.Is(page.err, io.EOF) {
				return read, page.err
			}
		}
	}
	return read, nil
}

func (w *packedRefReadWindow) page(start int64) *packedRefReadPage {
	for i := 0; i < w.count; i++ {
		if w.pages[i].start == start {
			return &w.pages[i]
		}
	}
	page := &w.pages[w.next]
	page.start = start
	page.n, page.err = w.reader.ReadAt(page.data[:], start)
	w.next = (w.next + 1) % len(w.pages)
	w.count = min(w.count+1, len(w.pages))
	return page
}
