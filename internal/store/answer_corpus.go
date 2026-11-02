package store

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
	// Registers the SQLite driver with database/sql for the owned corpus cache.
	_ "modernc.org/sqlite"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
)

const answerCorpusVersion = 1

// answerCorpusBusyTimeout is how long a corpus connection waits out another
// process's write or WAL recovery before failing SQLITE_BUSY. SQLite's
// default is no wait at all, so a reader racing the first opener after a
// killed writer failed at once with "database is locked (261)".
const answerCorpusBusyTimeout = 10 * time.Second

// CorpusRecord binds an answer document's path and digest to an exact source tip.
type CorpusRecord struct {
	ID         model.EntityID
	ShortID    string
	Path       string
	Tip        model.SHA
	TextDigest string
	UpdatedAt  time.Time
}

// AnswerCorpusCut retains owned canonical payloads independently of source caches.
type AnswerCorpusCut struct {
	Binding    string
	Generation string
	Records    []CorpusRecord
	db         *sql.DB
	tx         *sql.Tx
	byShortID  map[string]CorpusRecord
	skippedOps map[model.EntityID]int
	closed     atomic.Bool
	work       answerCorpusWork
}

type answerCorpusWork struct {
	metadataLoads      atomic.Int64
	canonicalRefreshes atomic.Int64
	payloadWrites      atomic.Int64
	payloadReads       atomic.Int64
	answerDecodes      atomic.Int64
}

type answerCorpusMetadataWorkKey struct{}

type answerCorpusSelection struct {
	sourceIdentity string
	binding        string
	generation     string
	identity       []byte
	selected       []queryMetadata
	records        []CorpusRecord
}

type answerCorpusWords struct {
	Title string
	Body  string
}

// OpenAnswerCorpus captures metadata before pinning canonical payload refreshes.
// The global query index is closed before the returned cut owns a read transaction.
func (s *Store) OpenAnswerCorpus(ctx context.Context, program, branch, cacheDir string) (cut *AnswerCorpusCut, err error) {
	var metadataLoads atomic.Int64
	ctx = context.WithValue(ctx, answerCorpusMetadataWorkKey{}, &metadataLoads)
	selection, err := s.selectAnswerCorpus(ctx, program, branch)
	if err != nil {
		return nil, err
	}
	cut, err = s.openAnswerCorpusSelection(ctx, selection, program, branch, cacheDir)
	if cut != nil {
		cut.work.metadataLoads.Store(metadataLoads.Load())
	}
	return cut, err
}

func (s *Store) openAnswerCorpusSelection(ctx context.Context, selection answerCorpusSelection, program, branch, cacheDir string) (cut *AnswerCorpusCut, err error) {
	if cacheDir == "" {
		cacheDir = filepath.Join(s.recordsCommonDir, "cc-notes", "answer-corpora-v1")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, err
	}
	key, err := json.Marshal([]string{selection.binding, program, branch})
	if err != nil {
		return nil, err
	}
	path, err := filepath.Abs(filepath.Join(cacheDir, answerCorpusDigest(key)+".sqlite"))
	if err != nil {
		return nil, err
	}
	lock, err := s.TrackingLock(ctx, "answer-corpus\x00"+path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: url.Values{"_pragma": {fmt.Sprintf("busy_timeout(%d)", answerCorpusBusyTimeout.Milliseconds())}}.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	db.SetMaxOpenConns(1)
	cut = &AnswerCorpusCut{Binding: selection.binding, Generation: selection.generation, Records: slices.Clone(selection.records), db: db}
	defer func() {
		err = errors.Join(err, lock.Close())
		if err != nil {
			err = errors.Join(err, cut.Close())
			cut = nil
		}
	}()
	var mode string
	if err = db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return cut, err
	}
	if mode != "wal" {
		return cut, fmt.Errorf("answer corpus journal mode is %q", mode)
	}
	cut.tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return cut, err
	}
	var header int
	if err = cut.tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='header' AND type='table'").Scan(&header); err != nil {
		return cut, err
	}
	if header == 1 {
		if err = validateAnswerCorpusHeader(ctx, cut.tx, selection.binding, program, branch); err != nil {
			return cut, err
		}
		err = validateAnswerCorpusCut(ctx, cut.tx, selection)
		if err == nil {
			cut.captureRecords(selection.selected)
			return cut, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return cut, err
		}
	}
	if err = cut.tx.Rollback(); err != nil {
		return cut, err
	}
	cut.tx = nil
	if err = s.materializeAnswerCorpus(ctx, cut, selection, program, branch); err != nil {
		return cut, err
	}
	after, err := s.answerCorpusSourceIdentity(ctx)
	if err != nil {
		return cut, err
	}
	if after != selection.sourceIdentity {
		return cut, errors.New("answer corpus source identity changed during materialization")
	}
	cut.tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return cut, err
	}
	if err = validateAnswerCorpusCut(ctx, cut.tx, selection); err != nil {
		return cut, err
	}
	cut.captureRecords(selection.selected)
	return cut, nil
}

func (c *AnswerCorpusCut) captureRecords(selected []queryMetadata) {
	c.byShortID = make(map[string]CorpusRecord, len(c.Records))
	for _, record := range c.Records {
		c.byShortID[record.ShortID] = record
	}
	c.skippedOps = make(map[model.EntityID]int, len(selected))
	for _, row := range selected {
		c.skippedOps[row.ID] = row.SkippedOps
	}
}

func (s *Store) selectAnswerCorpus(ctx context.Context, program, branch string) (answerCorpusSelection, error) {
	identity, err := s.answerCorpusSourceIdentity(ctx)
	if err != nil {
		return answerCorpusSelection{}, err
	}
	var callbackErr error
	selection, hit, err := indexedMetadataQuery(ctx, s, model.KindAnswer, func(tx *bolt.Tx, source queryMetadataSource) (answerCorpusSelection, error) {
		var rows []queryMetadata
		callbackErr = walkQueryMetadataLabel(tx, source.binding, source.kind, "scope:durable", false, func(row queryMetadata) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := validateQueryMetadata(tx, source, row); err != nil {
				return err
			}
			if len(row.SupersededBy) != 0 {
				return nil
			}
			if !slices.Contains(row.Labels, "program:"+program) && (branch == "" || !slices.Contains(row.Anchors, model.Anchor{Kind: model.AnchorBranch, Value: branch})) {
				return nil
			}
			if len(row.ID) != 40 || len(row.TextDigest) != 64 {
				return fmt.Errorf("answer corpus metadata is incomplete for %s", row.ID)
			}
			rows = append(rows, row)
			return nil
		})
		if callbackErr != nil {
			return answerCorpusSelection{}, callbackErr
		}
		return makeAnswerCorpusSelection(identity, source.binding, program, branch, rows)
	})
	if err != nil {
		return answerCorpusSelection{}, err
	}
	if callbackErr != nil {
		return answerCorpusSelection{}, callbackErr
	}
	if !hit {
		return answerCorpusSelection{}, errors.New("answer corpus metadata index is unavailable or inconsistent")
	}
	after, err := s.answerCorpusSourceIdentity(ctx)
	if err != nil {
		return answerCorpusSelection{}, err
	}
	if after != identity {
		return answerCorpusSelection{}, errors.New("answer corpus source identity changed during capture")
	}
	return selection, nil
}

func (s *Store) answerCorpusSourceIdentity(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.CheckRecords(); err != nil {
		return "", err
	}
	root, err := fileIDOf(s.recordsCommonDir)
	if err != nil {
		return "", err
	}
	entries, err := backendEntryIDs(s.recordsCommonDir)
	if err != nil {
		return "", err
	}
	config, backend := s.storage.stamps()
	return answerCorpusDigest(fmt.Appendf(nil, "%s\n%v\n%v\n%v\n%v\n%v\n%t\n%s", s.recordsCommonDir, root, entries, config, backend, s.storage.binding, s.storage.bound, foldCacheGeneration)), nil
}

func makeAnswerCorpusSelection(identity, metadataBinding, program, branch string, rows []queryMetadata) (answerCorpusSelection, error) {
	var programRows, branchRows []queryMetadata
	for _, row := range rows {
		if slices.Contains(row.Labels, "program:"+program) {
			programRows = append(programRows, row)
		}
		if branch != "" && slices.Contains(row.Anchors, model.Anchor{Kind: model.AnchorBranch, Value: branch}) {
			branchRows = append(branchRows, row)
		}
	}
	sortAnswerCorpusMetadata(programRows)
	sortAnswerCorpusMetadata(branchRows)
	selected := make([]queryMetadata, 0, len(rows))
	positions := make(map[model.EntityID]int, len(rows))
	for _, row := range slices.Concat(programRows, branchRows) {
		if position, ok := positions[row.ID]; ok {
			selected[position] = row
		} else {
			positions[row.ID] = len(selected)
			selected = append(selected, row)
		}
	}
	winners := make(map[string]CorpusRecord, len(selected))
	all := make([]CorpusRecord, 0, len(selected))
	for _, row := range selected {
		record := CorpusRecord{ID: row.ID, ShortID: row.ID.Short(), Path: row.ID.Short() + ".md", Tip: row.Tip, TextDigest: row.TextDigest, UpdatedAt: row.UpdatedTime}
		all = append(all, record)
		winners[record.ShortID] = record
	}
	records := make([]CorpusRecord, 0, len(winners))
	for _, record := range winners {
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b CorpusRecord) int { return cmp.Compare(a.Path, b.Path) })
	binding := answerCorpusDigest(fmt.Appendf(nil, "%d\n%s\n%s", answerCorpusVersion, identity, metadataBinding))
	data, err := json.Marshal(struct {
		Version int
		Binding string
		Program string
		Branch  string
		All     []CorpusRecord
		Records []CorpusRecord
	}{answerCorpusVersion, binding, program, branch, all, records})
	if err != nil {
		return answerCorpusSelection{}, err
	}
	return answerCorpusSelection{sourceIdentity: identity, binding: binding, generation: answerCorpusDigest(data), identity: data, selected: selected, records: records}, nil
}

func sortAnswerCorpusMetadata(rows []queryMetadata) {
	slices.SortFunc(rows, func(a, b queryMetadata) int {
		if order := b.UpdatedTime.Compare(a.UpdatedTime); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func answerCorpusDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Store) materializeAnswerCorpus(ctx context.Context, cut *AnswerCorpusCut, selection answerCorpusSelection, program, branch string) (err error) {
	conn, err := cut.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, rollbackErr := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
			err = errors.Join(err, rollbackErr)
		}
	}()
	if err := ensureAnswerCorpusSchema(ctx, conn, selection.binding, program, branch); err != nil {
		return err
	}
	var existing []byte
	err = conn.QueryRowContext(ctx, "SELECT identity FROM cut WHERE generation=?", selection.generation).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, selection.identity) {
			return errors.New("answer corpus generation identity differs")
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		pins := make(map[string]model.SHA, len(selection.selected))
		for _, row := range selection.selected {
			pins[refs.For(model.KindAnswer, row.ID)] = row.Tip
		}
		pinned := s.Pinned(pins)
		for _, row := range selection.selected {
			if err := materializeAnswerCorpusRecord(ctx, conn, pinned, cut, row); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO cut(generation,identity) VALUES(?,?)", selection.generation, selection.identity); err != nil {
			return err
		}
		for ordinal, record := range selection.records {
			data, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO member(generation,ordinal,record) VALUES(?,?,?)", selection.generation, ordinal, data); err != nil {
				return err
			}
		}
	} else {
		return err
	}
	if err := s.CheckRecords(); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func ensureAnswerCorpusSchema(ctx context.Context, conn *sql.Conn, binding, program, branch string) error {
	var tables int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table'").Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		for _, statement := range []string{
			"CREATE TABLE header(singleton INTEGER PRIMARY KEY CHECK(singleton=1),format INTEGER NOT NULL,binding TEXT NOT NULL,program TEXT NOT NULL,branch TEXT NOT NULL)",
			"CREATE TABLE payload(key TEXT PRIMARY KEY,digest TEXT NOT NULL,title BLOB NOT NULL,body BLOB NOT NULL) WITHOUT ROWID",
			"CREATE TABLE snapshot(tip TEXT PRIMARY KEY,id TEXT NOT NULL,digest TEXT NOT NULL,payload_key TEXT NOT NULL,metadata BLOB NOT NULL) WITHOUT ROWID",
			"CREATE TABLE cut(generation TEXT PRIMARY KEY,identity BLOB NOT NULL) WITHOUT ROWID",
			"CREATE TABLE member(generation TEXT NOT NULL,ordinal INTEGER NOT NULL,record BLOB NOT NULL,PRIMARY KEY(generation,ordinal)) WITHOUT ROWID",
		} {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, "INSERT INTO header VALUES(1,?,?,?,?)", answerCorpusVersion, binding, program, branch)
		return err
	}
	return validateAnswerCorpusHeader(ctx, conn, binding, program, branch)
}

type answerCorpusQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateAnswerCorpusHeader(ctx context.Context, query answerCorpusQuery, binding, program, branch string) error {
	var tables int
	if err := query.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table'").Scan(&tables); err != nil {
		return err
	}
	var format int
	var actualBinding, actualProgram, actualBranch string
	if err := query.QueryRowContext(ctx, "SELECT format,binding,program,branch FROM header WHERE singleton=1").Scan(&format, &actualBinding, &actualProgram, &actualBranch); err != nil {
		return err
	}
	if tables != 5 || format != answerCorpusVersion || actualBinding != binding || actualProgram != program || actualBranch != branch {
		return errors.New("answer corpus cache header differs")
	}
	return nil
}

func materializeAnswerCorpusRecord(ctx context.Context, conn *sql.Conn, pinned *Store, cut *AnswerCorpusCut, row queryMetadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var id model.EntityID
	var digest string
	var payloads int
	err := conn.QueryRowContext(ctx, "SELECT id,digest,(SELECT count(*) FROM payload WHERE payload.key=snapshot.payload_key AND payload.digest=snapshot.digest) FROM snapshot WHERE tip=?", row.Tip).Scan(&id, &digest, &payloads)
	if err == nil {
		if id != row.ID || digest != row.TextDigest || payloads != 1 {
			return errors.New("answer corpus snapshot proof differs")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cut.work.canonicalRefreshes.Add(1)
	snapshot, err := pinned.LoadAt(ctx, row.Tip)
	if err != nil {
		return err
	}
	answer, ok := snapshot.(model.Answer)
	if !ok || !reflect.DeepEqual(projectQueryMetadata(row.Binding, row.Tip, snapshot), row) {
		return fmt.Errorf("answer corpus canonical snapshot differs for %s", row.ID)
	}
	words, err := json.Marshal(answerCorpusWords{answer.Title, answer.Body})
	if err != nil {
		return err
	}
	key := answerCorpusDigest(words)
	result, err := conn.ExecContext(ctx, "INSERT INTO payload(key,digest,title,body) VALUES(?,?,?,?) ON CONFLICT(key) DO NOTHING", key, row.TextDigest, []byte(answer.Title), []byte(answer.Body))
	if err != nil {
		return err
	}
	written, err := result.RowsAffected()
	if err != nil {
		return err
	}
	cut.work.payloadWrites.Add(written)
	answer.Title, answer.Body = "", ""
	metadata, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO snapshot(tip,id,digest,payload_key,metadata) VALUES(?,?,?,?,?)", row.Tip, row.ID, row.TextDigest, key, metadata)
	return err
}

func validateAnswerCorpusCut(ctx context.Context, tx *sql.Tx, selection answerCorpusSelection) error {
	var identity []byte
	if err := tx.QueryRowContext(ctx, "SELECT identity FROM cut WHERE generation=?", selection.generation).Scan(&identity); err != nil {
		return err
	}
	if !bytes.Equal(identity, selection.identity) || answerCorpusDigest(identity) != selection.generation {
		return errors.New("answer corpus read view generation differs")
	}
	rows, err := tx.QueryContext(ctx, "SELECT ordinal,record FROM member WHERE generation=? ORDER BY ordinal", selection.generation)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	ordinal := 0
	for rows.Next() {
		var position int
		var data []byte
		if err := rows.Scan(&position, &data); err != nil {
			return err
		}
		var record CorpusRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if position != ordinal || ordinal >= len(selection.records) || !equalAnswerCorpusRecord(record, selection.records[ordinal]) {
			return errors.New("answer corpus read view membership differs")
		}
		ordinal++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if ordinal != len(selection.records) {
		return errors.New("answer corpus read view membership is incomplete")
	}
	return nil
}

func equalAnswerCorpusRecord(a, b CorpusRecord) bool {
	return a.ID == b.ID && a.ShortID == b.ShortID && a.Path == b.Path && a.Tip == b.Tip && a.TextDigest == b.TextDigest && a.UpdatedAt.Equal(b.UpdatedAt)
}

func (c *AnswerCorpusCut) read(ctx context.Context, shortID string, metadata bool) (model.Answer, error) {
	if err := ctx.Err(); err != nil {
		return model.Answer{}, err
	}
	if c.closed.Load() {
		return model.Answer{}, os.ErrClosed
	}
	record, ok := c.byShortID[shortID]
	if !ok {
		return model.Answer{}, fmt.Errorf("answer corpus %s: %w", shortID, ErrNotFound)
	}
	query := "SELECT snapshot.id,snapshot.digest,snapshot.payload_key,payload.title,payload.body FROM snapshot JOIN payload ON payload.key=snapshot.payload_key WHERE snapshot.tip=?"
	if metadata {
		query = "SELECT snapshot.id,snapshot.digest,snapshot.payload_key,payload.title,payload.body,snapshot.metadata FROM snapshot JOIN payload ON payload.key=snapshot.payload_key WHERE snapshot.tip=?"
	}
	var id model.EntityID
	var digest, key string
	var title, body, data []byte
	destinations := []any{&id, &digest, &key, &title, &body}
	if metadata {
		destinations = append(destinations, &data)
	}
	if err := c.tx.QueryRowContext(ctx, query, record.Tip).Scan(destinations...); err != nil {
		return model.Answer{}, err
	}
	c.work.payloadReads.Add(1)
	words, err := json.Marshal(answerCorpusWords{string(title), string(body)})
	if err != nil {
		return model.Answer{}, err
	}
	if id != record.ID || digest != record.TextDigest || answerCorpusDigest(words) != key || answerCorpusDigest([]byte(render.AnswerMarkdown(string(title), string(body)))) != digest {
		return model.Answer{}, errors.New("answer corpus captured payload differs")
	}
	answer := model.Answer{ID: id}
	if metadata {
		if err := json.Unmarshal(data, &answer); err != nil {
			return model.Answer{}, err
		}
		c.work.answerDecodes.Add(1)
		if answer.ID != record.ID || answer.Head != record.Tip || answer.Title != "" || answer.Body != "" {
			return model.Answer{}, errors.New("answer corpus captured answer differs")
		}
		answer.SkippedOps = c.skippedOps[record.ID]
	}
	answer.Title, answer.Body = string(title), string(body)
	return answer, nil
}

// Text reads and verifies one captured Markdown payload without consulting Git.
func (c *AnswerCorpusCut) Text(ctx context.Context, shortID string) (string, error) {
	answer, err := c.read(ctx, shortID, false)
	if err != nil {
		return "", err
	}
	return render.AnswerMarkdown(answer.Title, answer.Body), nil
}

// Pick hydrates canonical answers from the retained view in requested hit order.
func (c *AnswerCorpusCut) Pick(ctx context.Context, shortIDs []string) ([]model.Answer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed.Load() {
		return nil, os.ErrClosed
	}
	answers := make([]model.Answer, 0, len(shortIDs))
	for _, id := range shortIDs {
		answer, err := c.read(ctx, id, true)
		if err != nil {
			return nil, err
		}
		answers = append(answers, answer)
	}
	return answers, nil
}

// Close releases every owned handle and is safe to call more than once.
func (c *AnswerCorpusCut) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	var err error
	if c.tx != nil {
		err = c.tx.Rollback()
		if errors.Is(err, sql.ErrTxDone) {
			err = nil
		}
	}
	return errors.Join(err, c.db.Close())
}
