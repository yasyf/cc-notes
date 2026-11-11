package notes

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestMatchAnswersRanksAnswersSharingTheSameWordsAlikeOnEveryCall(t *testing.T) {
	words := strings.Fields("quokka zephyr marlin osprey tapir ibex lemur gecko")
	title := "Approve the window update: " + strings.Join(words, " ")
	newer := model.Answer{ID: "b000000", Title: title, Body: "Approved.", UpdatedAt: 2}
	older := model.Answer{ID: "a000000", Title: title, Body: "Approved.", UpdatedAt: 1}
	answers := []model.Answer{older, newer}
	for i, word := range words {
		for j := range i {
			answers = append(answers, model.Answer{ID: model.EntityID(fmt.Sprintf("w%d-%d", i, j)), Title: "Filler on the " + word})
		}
	}
	for i := len(answers); i < 250; i++ {
		answers = append(answers, model.Answer{ID: model.EntityID(fmt.Sprintf("f%d", i)), Title: "Filler on the cache"})
	}
	text := "Should the window update land? " + strings.Join(words, " ")
	for call := range 100 {
		found := matchAnswers(answers, text, 2, true)
		if len(found) != 2 || found[0].Answer.ID != newer.ID || found[1].Answer.ID != older.ID || found[0].Score != found[1].Score {
			t.Fatalf("call %d ranked %+v, want the newer then the older answer at one score", call, found)
		}
	}
}
