package notes

import (
	"cmp"
	"context"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/answermatch"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const (
	// MatchPhrasePrefix opens a label naming a phrase that ties an answer to any
	// text containing it, whatever the ranking says of the rest.
	MatchPhrasePrefix = "match:"

	matchRareShare   = 25
	matchRareFloor   = 2
	matchTitleTerms  = 2
	matchBodyTerms   = 3
	matchTitleRarity = 1.4
	matchBodyRarity  = 2.0
	matchTitleBoost  = 3
	matchLengthBias  = 10
)

// AnswerMatch is one answer a text touches: the phrase label found in the text,
// or the rare terms the two share.
type AnswerMatch struct {
	Answer model.Answer
	Phrase string
	Terms  []string
	Score  float64
}

// MatchAnswers ranks the live, unexpired answers against the words of text, so
// an answer reaches a reader with no anchor and no search of their own. Phrase
// matches come first. A word's rarity runs from 1 for a word only one answer
// uses down to 0 for one they all use. Any other answer needs two rare words of
// text in its question or plain labels whose rarities sum to 1.4, or three
// anywhere that sum to 2 and make up at least half of text's rare words. Those
// floors keep a record that merely shares a word or two from drawing a ruling,
// and the result stays at f's limit however many answers the store holds.
// Scores sum in term order, so answers sharing the same words with text score
// alike and a text ranks the same answers in the same order on every call.
func (c *Client) MatchAnswers(ctx context.Context, text string, f SearchFilter) ([]AnswerMatch, error) {
	return c.rankAnswers(ctx, text, f, true)
}

// NearestAnswers ranks the same answers by the same score as MatchAnswers
// without its floors: every answer sharing one rare word with text competes,
// and f's limit keeps the best. It is for a reader who checks each hit, such as
// a gate asking whether a question is already answered, where a long text would
// otherwise never reach a ruling whose subject sits only in its body.
func (c *Client) NearestAnswers(ctx context.Context, text string, f SearchFilter) ([]AnswerMatch, error) {
	return c.rankAnswers(ctx, text, f, false)
}

func (c *Client) rankAnswers(ctx context.Context, text string, f SearchFilter, floored bool) ([]AnswerMatch, error) {
	asked := slices.Sorted(maps.Keys(matchTerms(text)))
	corpus, err := c.s.IndexedAnswerMatches(ctx, f.Labels, asked)
	if err != nil {
		return nil, err
	}
	found := scoreAnswerMatches(corpus, text, asked, f.Limit, floored)
	rows := make([]store.AnswerMatchRow, len(found))
	for i, match := range found {
		rows[i] = store.AnswerMatchRow{ID: match.Answer.ID, Tip: match.Answer.Head}
	}
	answers, err := c.s.MaterializeAnswerMatches(ctx, rows)
	if err != nil {
		return nil, err
	}
	for i := range found {
		found[i].Answer = answers[i]
	}
	return found, nil
}

func scoreAnswerMatches(corpus store.AnswerMatchCorpus, text string, asked []string, limit int, floored bool) []AnswerMatch {
	lowered := strings.ToLower(text)
	seen := corpus.Seen
	answers := corpus.Answers

	rare := max(matchRareFloor, len(answers)/matchRareShare)
	askedRare := 0
	for _, term := range asked {
		if seen[term] <= rare {
			askedRare++
		}
	}
	allAnswers := float64(len(answers) + 1)
	var found []AnswerMatch
	for _, a := range answers {
		match := AnswerMatch{Answer: model.Answer{ID: a.ID, UpdatedAt: a.UpdatedAt, Head: a.Tip}, Phrase: phraseIn(a.Labels, lowered)}
		inQuestion := 0
		var questionRarity, rarity float64
		for _, term := range asked {
			question := a.Terms[term]&1 != 0
			body := a.Terms[term]&2 != 0
			if (!question && !body) || seen[term] > rare {
				continue
			}
			weight := math.Log(allAnswers / float64(seen[term]))
			rarity += weight / math.Log(allAnswers)
			if question {
				inQuestion++
				questionRarity += weight / math.Log(allAnswers)
				weight *= matchTitleBoost
			}
			match.Terms = append(match.Terms, term)
			match.Score += weight
		}
		namedInQuestion := inQuestion >= matchTitleTerms && questionRarity >= matchTitleRarity
		mostOfText := len(match.Terms) >= matchBodyTerms && rarity >= matchBodyRarity && len(match.Terms)*2 >= askedRare
		if match.Phrase == "" && !namedInQuestion && !mostOfText && (floored || len(match.Terms) == 0) {
			continue
		}
		match.Score /= math.Log(float64(matchLengthBias + a.QuestionCount + a.BodyCount))
		found = append(found, match)
	}
	slices.SortFunc(found, func(a, b AnswerMatch) int {
		if c := cmp.Compare(boolRank(b.Phrase != ""), boolRank(a.Phrase != "")); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Answer.UpdatedAt, a.Answer.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.Answer.ID, b.Answer.ID)
	})
	if limit >= 0 && len(found) > limit {
		found = found[:limit]
	}
	return found
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func phraseIn(tags []string, lowered string) string {
	for _, tag := range tags {
		if phrase, labelled := strings.CutPrefix(tag, MatchPhrasePrefix); labelled && strings.Contains(lowered, strings.ToLower(phrase)) {
			return phrase
		}
	}
	return ""
}

func matchTerms(text string) map[string]struct{} {
	return answermatch.Terms(text)
}
