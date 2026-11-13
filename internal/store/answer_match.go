package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// AnswerMatchRow retains ranking inputs and the exact source tip without answer bodies.
type AnswerMatchRow struct {
	ID            model.EntityID
	Tip           model.SHA
	UpdatedAt     int64
	Labels        []string
	QuestionCount int
	BodyCount     int
	Terms         map[string]uint8 `json:"-"`
	Version       int
	Binding       string
	Deleted       bool
	Superseded    bool
	StaleAt       int64
	SkippedOps    int `json:"skipped_ops"`
}

// AnswerMatchCorpus counts requested-term unions within the selected live cohort.
type AnswerMatchCorpus struct {
	Answers []AnswerMatchRow
	Seen    map[string]int
}

func answerMatchRow(row queryMetadata) AnswerMatchRow {
	return AnswerMatchRow{
		ID: row.ID, Tip: row.Tip, UpdatedAt: row.UpdatedAt, Labels: row.Labels,
		QuestionCount: len(row.AnswerQuestionTerms), BodyCount: len(row.AnswerBodyTerms),
		Version: row.Version, Binding: row.Binding, Deleted: row.Deleted,
		Superseded: len(row.SupersededBy) != 0, StaleAt: row.StaleAt, SkippedOps: row.SkippedOps,
	}
}

func (q queryMetadataSource) matchValues(tx *bolt.Tx, index, prefix []byte, visit func([]byte) error) error {
	if q.retained != nil {
		return q.retained.prefix(queryPostingKey(index, prefix), visit)
	}
	binding := queryMetadataBinding(tx, q.binding)
	if binding == nil {
		return nil
	}
	cursor := binding.Bucket(index).Cursor()
	for key, value := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, value = cursor.Next() {
		if err := visit(value); err != nil {
			return err
		}
	}
	return nil
}

func (q queryMetadataSource) matchRow(tx *bolt.Tx, id model.EntityID) ([]byte, error) {
	if q.retained != nil {
		return q.retained.get(queryPostingKey(queryMetadataAnswerRankingBucket, []byte(id)))
	}
	return queryMetadataBinding(tx, q.binding).Bucket(queryMetadataAnswerRankingBucket).Get([]byte(id)), nil
}

// IndexedAnswerMatches selects compact rows and intersects requested postings at one generation.
func (s *Store) IndexedAnswerMatches(ctx context.Context, labels, asked []string) (AnswerMatchCorpus, error) {
	corpus, hit, err := indexedMetadataQuery(ctx, s, model.KindAnswer, func(tx *bolt.Tx, source queryMetadataSource) (AnswerMatchCorpus, error) {
		corpus := AnswerMatchCorpus{Seen: make(map[string]int, len(asked))}
		selected := make(map[model.EntityID]int)
		add := func(data []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			var row AnswerMatchRow
			if err := json.Unmarshal(data, &row); err != nil {
				return err
			}
			tip, err := queryMetadataRowTip(tx, source, row.ID)
			if err != nil {
				return err
			}
			if row.Version != queryMetadataVersion || row.Binding != source.binding || row.Tip == "" || row.Tip != tip {
				return fmt.Errorf("answer ranking metadata differs from captured %s", row.ID)
			}
			if row.Deleted || row.Superseded || row.StaleAt != 0 {
				return nil
			}
			for _, label := range labels {
				if !slices.Contains(row.Labels, label) {
					return nil
				}
			}
			selected[row.ID] = len(corpus.Answers)
			corpus.Answers = append(corpus.Answers, row)
			return nil
		}
		var err error
		if len(labels) == 0 {
			err = source.matchValues(tx, queryMetadataAnswerRankingBucket, nil, add)
		} else {
			prefix := append(queryMetadataLabelPrefix(model.KindAnswer, labels[0]), 0)
			err = source.matchValues(tx, queryMetadataLabelsBucket, prefix, func(id []byte) error {
				data, err := source.matchRow(tx, model.EntityID(id))
				if err != nil {
					return err
				}
				return add(data)
			})
		}
		if err != nil {
			return corpus, err
		}
		for _, term := range asked {
			for i, index := range [][]byte{queryMetadataQuestionTermsBucket, queryMetadataBodyTermsBucket} {
				prefix := append(queryMetadataLabelPrefix(model.KindAnswer, term), 0)
				if err := source.matchValues(tx, index, prefix, func(id []byte) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					position, ok := selected[model.EntityID(id)]
					if !ok {
						return nil
					}
					row := &corpus.Answers[position]
					if row.Terms == nil {
						row.Terms = make(map[string]uint8)
					}
					if row.Terms[term] == 0 {
						corpus.Seen[term]++
					}
					row.Terms[term] |= 1 << i
					return nil
				}); err != nil {
					return corpus, err
				}
			}
		}
		return corpus, ctx.Err()
	})
	if err != nil {
		return AnswerMatchCorpus{}, err
	}
	if !hit {
		return AnswerMatchCorpus{}, managed.ErrReindex
	}
	return corpus, nil
}

// MaterializeAnswerMatches folds only final hits at their captured ranking tips.
func (s *Store) MaterializeAnswerMatches(ctx context.Context, rows []AnswerMatchRow) ([]model.Answer, error) {
	if err := s.CheckRecords(); err != nil {
		return nil, err
	}
	entries := make([]tipEntry, len(rows))
	for i, row := range rows {
		entries[i] = tipEntry{ref: refs.For(model.KindAnswer, row.ID), tip: row.Tip}
	}
	return foldAll(ctx, s, entries, fold.Answer)
}
