package notes

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/answermatch"
	"github.com/yasyf/cc-notes/model"
)

func matchAnswers(answers []model.Answer, text string, limit int, floored bool) []AnswerMatch {
	lowered := strings.ToLower(text)
	asked := slices.Sorted(maps.Keys(matchTerms(text)))
	questions := make([]map[string]struct{}, len(answers))
	bodies := make([]map[string]struct{}, len(answers))
	seen := map[string]int{}
	for i, a := range answers {
		questions[i] = answermatch.QuestionTerms(a.Title, a.Tags)
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
	for _, term := range asked {
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
		for _, term := range asked {
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
