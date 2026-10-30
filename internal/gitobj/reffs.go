package gitobj

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5"
)

// refsDir is the ref tree's root inside a git directory. go-git keeps its own
// spelling unexported.
const refsDir = "refs"

const (
	packDir         = "objects/pack"
	packPrefix      = "pack-"
	loosePackPrefix = "loose-"
	packIndexExt    = ".idx"
	packIndexBuffer = 1 << 16
)

// gitDirFS adapts a git directory to what go-git's filesystem storage reads.
// It withholds ref lock files from listings of the ref tree: git's own
// enumeration never treats a .lock entry as a ref, while go-git's loose-ref
// walk aborts the whole listing on the first empty file. It also serves the
// loose-<hash> packs git maintenance's loose-objects task writes under their
// pack-<hash> names, because go-git only ever lists and opens pack-<hash>.
type gitDirFS struct {
	billy.Filesystem
}

func (fs gitDirFS) ReadDir(path string) ([]os.FileInfo, error) {
	if underRefs(path) {
		if _, err := fs.Filesystem.Stat(path); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(filepath.Join(fs.Root(), path))
		if err != nil {
			return nil, err
		}
		return referenceFileInfos(entries)
	}
	entries, err := fs.Filesystem.ReadDir(path)
	if err != nil {
		return entries, err
	}
	if filepath.Clean(path) == packDir {
		for i, entry := range entries {
			if rest, ok := strings.CutPrefix(entry.Name(), loosePackPrefix); ok {
				entries[i] = renamedInfo{FileInfo: entry, name: packPrefix + rest}
			}
		}
	}
	return entries, nil
}

func referenceFileInfos(entries []os.DirEntry) ([]os.FileInfo, error) {
	infos := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func (fs gitDirFS) Open(path string) (billy.File, error) {
	file, err := fs.Filesystem.Open(path)
	if loose, ok := loosePackPath(path); ok && errors.Is(err, os.ErrNotExist) {
		file, err = fs.Filesystem.Open(loose)
	}
	if err != nil || !packIndexPath(path) {
		return file, err
	}
	return &packIndexFile{File: file, reader: bufio.NewReaderSize(file, packIndexBuffer)}, nil
}

type packIndexFile struct {
	billy.File
	reader *bufio.Reader
}

func (f *packIndexFile) Read(p []byte) (int, error) {
	return f.reader.Read(p)
}

func (f *packIndexFile) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekCurrent {
		offset -= int64(f.reader.Buffered())
	}
	pos, err := f.File.Seek(offset, whence)
	if err == nil {
		f.reader.Reset(f.File)
	}
	return pos, err
}

func packIndexPath(path string) bool {
	dir, base := filepath.Split(filepath.Clean(path))
	return filepath.Clean(dir) == packDir && filepath.Ext(base) == packIndexExt
}

func (fs gitDirFS) Stat(path string) (os.FileInfo, error) {
	info, err := fs.Filesystem.Stat(path)
	if loose, ok := loosePackPath(path); ok && errors.Is(err, os.ErrNotExist) {
		return fs.Filesystem.Stat(loose)
	}
	return info, err
}

func loosePackPath(path string) (string, bool) {
	dir, base := filepath.Split(filepath.Clean(path))
	rest, ok := strings.CutPrefix(base, packPrefix)
	if !ok || filepath.Clean(dir) != packDir {
		return "", false
	}
	return filepath.Join(packDir, loosePackPrefix+rest), true
}

type renamedInfo struct {
	os.FileInfo
	name string
}

func (info renamedInfo) Name() string { return info.name }

func underRefs(path string) bool {
	clean := filepath.Clean(path)
	return clean == refsDir || strings.HasPrefix(clean, refsDir+string(filepath.Separator))
}
