package store

import (
	"bytes"
	"container/list"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	_ "modernc.org/sqlite"
)

const cacheTokenSize = 64

const cacheSchema = `CREATE TABLE IF NOT EXISTS cache_meta (id INTEGER PRIMARY KEY CHECK(id = 1), capacity INTEGER NOT NULL, next_slot INTEGER NOT NULL, rank INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS cache_entries (name TEXT PRIMARY KEY, slot INTEGER NOT NULL UNIQUE, token BLOB NOT NULL, rank INTEGER NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS cache_free (slot INTEGER PRIMARY KEY);`

type cacheHit struct {
	name string
	rank uint64
}

type lruDir struct {
	capacity int
	dir      string

	writer sync.Mutex
	mu     sync.Mutex
	active bool
	rank   uint64
	order  *list.List
	hits   map[string]*list.Element
	dbMu   sync.Mutex
	db     *sql.DB
	dbInfo os.FileInfo
	closed bool
}

func (l *lruDir) read(name string) ([]byte, bool) {
	f, ok := l.open(name)
	if !ok {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	return data, err == nil
}

func (l *lruDir) open(name string) (*os.File, bool) {
	db, err := l.database(true)
	if err != nil {
		return nil, false
	}
	var slot int
	var token []byte
	if db.QueryRow("SELECT slot, token FROM cache_entries WHERE name = ?", name).Scan(&slot, &token) != nil {
		return nil, false
	}
	f, err := os.Open(l.slotPath(slot))
	if err != nil {
		return nil, false
	}
	var header [cacheTokenSize]byte
	key := sha256.Sum256([]byte(name))
	if _, err := io.ReadFull(f, header[:]); err != nil || !bytes.Equal(header[:32], key[:]) || !bytes.Equal(header[:], token) {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

func (l *lruDir) ensure() bool {
	file, err := l.writerFile()
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func (l *lruDir) writerFile() (*os.File, error) {
	if err := os.MkdirAll(l.dir, 0o750); err != nil {
		return nil, err
	}
	return os.OpenFile(l.dir+".lock", os.O_CREATE|os.O_RDWR, 0o600)
}

func (l *lruDir) lockWriter() (*os.File, bool) {
	file, err := l.writerFile()
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, false
	}
	return file, true
}

func (l *lruDir) database(readOnly bool) (*sql.DB, error) {
	l.dbMu.Lock()
	defer l.dbMu.Unlock()
	if l.closed {
		return nil, sql.ErrConnDone
	}
	path := filepath.Join(l.dir, "index.sqlite")
	if l.db != nil {
		info, err := os.Stat(path)
		if err == nil && os.SameFile(info, l.dbInfo) {
			return l.db, nil
		}
		if err := l.db.Close(); err != nil {
			return nil, err
		}
		l.db = nil
	}
	mode := "rwc"
	if readOnly {
		mode = "rw"
	}
	location := url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Set("mode", mode)
	query.Set("_txlock", "immediate")
	query.Add("_pragma", "busy_timeout(25)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Add("_pragma", "wal_autocheckpoint(64)")
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if readOnly {
		var capacity int
		if err := db.QueryRow("SELECT capacity FROM cache_meta WHERE id = 1").Scan(&capacity); err != nil {
			return nil, errors.Join(err, db.Close())
		}
		if capacity != l.capacity {
			return nil, errors.Join(fmt.Errorf("cache capacity %d, expected %d", capacity, l.capacity), db.Close())
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, errors.Join(err, db.Close())
		}
		l.db, l.dbInfo = db, info
		return db, nil
	}
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if strings.ToLower(journal) != "wal" {
		return nil, errors.Join(fmt.Errorf("cache journal mode %q", journal), db.Close())
	}
	if _, err := db.Exec(cacheSchema); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO cache_meta VALUES (1, ?, 0, 0)", l.capacity); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	var capacity int
	if err := db.QueryRow("SELECT capacity FROM cache_meta WHERE id = 1").Scan(&capacity); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if capacity != l.capacity {
		return nil, errors.Join(fmt.Errorf("cache capacity %d, expected %d", capacity, l.capacity), db.Close())
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	l.db, l.dbInfo = db, info
	return db, nil
}

func (l *lruDir) close() error {
	l.writer.Lock()
	defer l.writer.Unlock()
	l.dbMu.Lock()
	defer l.dbMu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.db == nil {
		return nil
	}
	return l.db.Close()
}

func (l *lruDir) write(name string, data []byte) {
	if l.capacity <= 0 {
		return
	}
	l.writer.Lock()
	defer l.writer.Unlock()
	lock, ok := l.lockWriter()
	if !ok {
		return
	}
	defer func() { _ = lock.Close() }()
	db, err := l.database(false)
	if err != nil {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	hits := l.pending()
	var rank int64
	if tx.QueryRow("SELECT rank FROM cache_meta WHERE id = 1").Scan(&rank) != nil {
		return
	}
	for _, hit := range hits {
		rank++
		if _, err := tx.Exec("UPDATE cache_entries SET rank = ? WHERE name = ?", rank, hit.name); err != nil {
			return
		}
	}
	slot, err := l.allocate(tx, name)
	if err != nil {
		return
	}
	key := sha256.Sum256([]byte(name))
	var token [cacheTokenSize]byte
	copy(token[:32], key[:])
	if _, err := rand.Read(token[32:]); err != nil {
		return
	}
	if !l.publish(slot, token[:], data) {
		return
	}
	rank++
	if _, err := tx.Exec("INSERT INTO cache_entries VALUES (?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET slot=excluded.slot, token=excluded.token, rank=excluded.rank", name, slot, token[:], rank); err != nil {
		return
	}
	if _, err := tx.Exec("UPDATE cache_meta SET rank = ? WHERE id = 1", rank); err != nil {
		return
	}
	if tx.Commit() != nil {
		return
	}
	l.acknowledge(hits)
}

func (l *lruDir) allocate(tx *sql.Tx, name string) (int, error) {
	var slot int
	err := tx.QueryRow("SELECT slot FROM cache_entries WHERE name = ?", name).Scan(&slot)
	if err == nil {
		return slot, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	err = tx.QueryRow("SELECT slot FROM cache_free ORDER BY slot LIMIT 1").Scan(&slot)
	if err == nil {
		_, err = tx.Exec("DELETE FROM cache_free WHERE slot = ?", slot)
		return slot, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err := tx.QueryRow("SELECT next_slot FROM cache_meta WHERE id = 1").Scan(&slot); err != nil {
		return 0, err
	}
	if slot < l.capacity {
		_, err = tx.Exec("UPDATE cache_meta SET next_slot = next_slot + 1 WHERE id = 1")
		return slot, err
	}
	if err := tx.QueryRow("SELECT slot FROM cache_entries ORDER BY rank LIMIT 1").Scan(&slot); err != nil {
		return 0, err
	}
	_, err = tx.Exec("DELETE FROM cache_entries WHERE slot = ?", slot)
	return slot, err
}

func (l *lruDir) slotPath(slot int) string {
	return filepath.Join(l.dir, fmt.Sprintf("slot-%08x", slot))
}

func (l *lruDir) publish(slot int, token, data []byte) bool {
	pending := filepath.Join(l.dir, ".pending")
	f, err := os.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false
	}
	if _, err := f.Write(token); err != nil {
		_ = f.Close()
		return false
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return false
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return false
	}
	if f.Close() != nil {
		return false
	}
	return os.Rename(pending, l.slotPath(slot)) == nil
}

func (l *lruDir) touch(name string) {
	l.mu.Lock()
	active := l.active
	l.mu.Unlock()
	if !active {
		return
	}
	db, err := l.database(true)
	if err != nil {
		return
	}
	var present int
	if db.QueryRow("SELECT 1 FROM cache_entries WHERE name = ?", name).Scan(&present) != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rank++
	hit := cacheHit{name, l.rank}
	if element, ok := l.hits[name]; ok {
		element.Value = hit
		l.order.MoveToBack(element)
		return
	}
	l.hits[name] = l.order.PushBack(hit)
	if l.order.Len() > l.capacity {
		delete(l.hits, l.order.Remove(l.order.Front()).(cacheHit).name)
	}
}

func (l *lruDir) pending() []cacheHit {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		return nil
	}
	hits := make([]cacheHit, 0, l.order.Len())
	for element := l.order.Front(); element != nil; element = element.Next() {
		hits = append(hits, element.Value.(cacheHit))
	}
	return hits
}

func (l *lruDir) acknowledge(hits []cacheHit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		l.order = list.New()
		l.hits = make(map[string]*list.Element)
		l.active = true
		return
	}
	for _, hit := range hits {
		if element, ok := l.hits[hit.name]; ok && element.Value.(cacheHit).rank == hit.rank {
			l.order.Remove(element)
			delete(l.hits, hit.name)
		}
	}
}

func (l *lruDir) names() []string {
	db, err := l.database(true)
	if err != nil {
		return nil
	}
	rows, err := db.Query("SELECT name FROM cache_entries ORDER BY name")
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			return nil
		}
		names = append(names, name)
	}
	if rows.Err() != nil {
		return nil
	}
	return names
}

func (l *lruDir) remove(name string) {
	l.writer.Lock()
	defer l.writer.Unlock()
	lock, ok := l.lockWriter()
	if !ok {
		return
	}
	defer func() { _ = lock.Close() }()
	db, err := l.database(false)
	if err != nil {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("INSERT INTO cache_free SELECT slot FROM cache_entries WHERE name = ?", name); err != nil {
		return
	}
	if _, err := tx.Exec("DELETE FROM cache_entries WHERE name = ?", name); err != nil {
		return
	}
	if tx.Commit() != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if element, ok := l.hits[name]; ok {
		l.order.Remove(element)
		delete(l.hits, name)
	}
}
