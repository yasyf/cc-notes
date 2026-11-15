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
	names := answermatch.Names(text)
	asked := slices.Sorted(maps.Keys(names))
	questions := make([]map[string]struct{}, len(answers))
	bodies := make([]map[string]struct{}, len(answers))
	seen := map[string]int{}
	for i, a := range answers {
		questions[i] = answermatch.QuestionTerms(a.Title, a.Tags)
		bodies[i] = answermatch.Terms(a.Body)
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
	numbers := map[int]int{}
	rareNames := map[int]struct{}{}
	for _, term := range asked {
		if _, numbered := numbers[names[term]]; !numbered {
			numbers[names[term]] = len(numbers)
		}
		if seen[term] <= rare {
			rareNames[names[term]] = struct{}{}
		}
	}
	allAnswers := float64(len(answers) + 1)
	var found []AnswerMatch
	for i, a := range answers {
		match := AnswerMatch{Answer: a, Phrase: phraseIn(a.Tags, lowered)}
		shared := make([]nameRarity, len(numbers))
		for _, term := range asked {
			_, question := questions[i][term]
			_, body := bodies[i][term]
			if (!question && !body) || seen[term] > rare {
				continue
			}
			weight := math.Log(allAnswers / float64(seen[term]))
			name := &shared[numbers[names[term]]]
			name.anywhere = max(name.anywhere, weight/math.Log(allAnswers))
			if question {
				name.question = max(name.question, weight/math.Log(allAnswers))
				weight *= matchTitleBoost
			}
			match.Terms = append(match.Terms, term)
			match.Score += weight
		}
		namedInQuestion, mostOfText := sharedFloors(shared, len(rareNames))
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
