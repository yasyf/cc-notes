package notes

import (
	"cmp"
	"context"
	"math"
	"regexp"
	"slices"
	"strings"

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
	matchMinTerm     = 3
	matchStatusLen   = 3
)

var (
	matchWord       = regexp.MustCompile(`[a-z0-9]+(?:[.-][a-z0-9]+)*`)
	matchLetterNum  = regexp.MustCompile(`([a-z])([0-9])`)
	matchNumLetter  = regexp.MustCompile(`([0-9])([a-z])`)
	matchWordJoiner = regexp.MustCompile(`[.-]`)
	matchStopWords  = func() map[string]struct{} {
		words := map[string]struct{}{}
		for _, word := range strings.Fields("the and for that with this from you are not but all any can has have was were its our his her him she they them their what when which who how why will would should could into out over been being than then there here just only also more most some such very each both about after before because while where does did done yes get got use used using one two per via own new old") {
			words[word] = struct{}{}
		}
		return words
	}()
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
	answers, err := c.s.ListAnswers(ctx, false, false)
	if err != nil {
		return nil, err
	}
	return matchAnswers(slices.DeleteFunc(answers, func(a model.Answer) bool {
		return a.StaleAt != 0 || !hasAll(a.Tags, f.Labels)
	}), text, f.Limit, floored), nil
}

func matchAnswers(answers []model.Answer, text string, limit int, floored bool) []AnswerMatch {
	lowered := strings.ToLower(text)
	asked := matchTerms(text)
	questions := make([]map[string]struct{}, len(answers))
	bodies := make([]map[string]struct{}, len(answers))
	seen := map[string]int{}
	for i, a := range answers {
		questions[i] = matchTerms(a.Title + " " + strings.Join(plainLabels(a.Tags), " "))
		bodies[i] = matchTerms(a.Body)
		for term := range questions[i] {
			seen[term]++
		}
		for term := range bodies[i] {
			if _, counted := questions[i][term]; !counted {
				seen[term]++
			}
		}
	}
	rare := max(matchRareFloor, len(answers)/matchRareShare)
	askedRare := 0
	for term := range asked {
		if seen[term] <= rare {
			askedRare++
		}
	}
	allAnswers := float64(len(answers) + 1)
	var found []AnswerMatch
	for i, a := range answers {
		match := AnswerMatch{Answer: a, Phrase: phraseIn(a.Tags, lowered)}
		inQuestion := 0
		var questionRarity, rarity float64
		for term := range asked {
			_, question := questions[i][term]
			_, body := bodies[i][term]
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
		match.Score /= math.Log(float64(matchLengthBias + len(questions[i]) + len(bodies[i])))
		slices.Sort(match.Terms)
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

func plainLabels(tags []string) []string {
	return slices.DeleteFunc(slices.Clone(tags), func(tag string) bool { return strings.Contains(tag, ":") })
}

// matchTerms splits text into its comparable words: lowercased, split where a
// letter meets a digit so "edge403" reads as edge and 403, with each dotted or
// hyphenated word also kept whole, and a trailing plural s dropped. A number
// counts only at three digits, the width of a status code.
func matchTerms(text string) map[string]struct{} {
	spaced := matchNumLetter.ReplaceAllString(matchLetterNum.ReplaceAllString(strings.ToLower(text), "$1 $2"), "$1 $2")
	terms := map[string]struct{}{}
	add := func(word string) {
		_, stop := matchStopWords[word]
		if stop || len(word) < matchMinTerm || (isDigits(word) && len(word) != matchStatusLen) {
			return
		}
		if len(word) > matchMinTerm && strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") {
			word = strings.TrimSuffix(word, "s")
		}
		terms[word] = struct{}{}
	}
	for _, word := range matchWord.FindAllString(spaced, -1) {
		add(word)
		for _, part := range matchWordJoiner.Split(word, -1) {
			add(part)
		}
	}
	return terms
}

func isDigits(word string) bool {
	return strings.Trim(word, "0123456789") == ""
}
