// Package answermatch defines lexical terms shared by answer ranking and its persistent index.
package answermatch

import (
	"slices"
	"strings"
)

const (
	matchMinTerm   = 3
	matchStatusLen = 3
)

var matchStopWords = func() map[string]struct{} {
	words := map[string]struct{}{}
	for _, word := range strings.Fields("the and for that with this from you are not but all any can has have was were its our his her him she they them their what when which who how why will would should could into out over been being than then there here just only also more most some such very each both about after before because while where does did done yes get got use used using one two per via own new old") {
		words[word] = struct{}{}
	}
	return words
}()

// QuestionTerms combines the title with plain labels, excluding labels containing a colon.
func QuestionTerms(title string, tags []string) map[string]struct{} {
	labels := slices.DeleteFunc(slices.Clone(tags), func(tag string) bool { return strings.Contains(tag, ":") })
	return Terms(title + " " + strings.Join(labels, " "))
}

// Terms extracts normalized ASCII words and three-digit status codes, including joined word parts.
func Terms(text string) map[string]struct{} {
	text = strings.ToLower(text)
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
	for i := 0; i < len(text); {
		if !matchAlphaNum(text[i]) {
			i++
			continue
		}
		start := i
		i++
		for i < len(text) {
			if matchAlphaNum(text[i]) {
				if matchDigit(text[i]) != matchDigit(text[i-1]) {
					break
				}
				i++
				continue
			}
			if (text[i] == '.' || text[i] == '-') && i+1 < len(text) && matchAlphaNum(text[i+1]) {
				i += 2
				continue
			}
			break
		}
		add(text[start:i])
		part := start
		for j := start; j < i; j++ {
			if text[j] == '.' || text[j] == '-' {
				add(text[part:j])
				part = j + 1
			}
		}
		if part != start {
			add(text[part:i])
		}
	}
	return terms
}

func matchAlphaNum(b byte) bool {
	return b >= 'a' && b <= 'z' || matchDigit(b)
}

func matchDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isDigits(word string) bool {
	return strings.Trim(word, "0123456789") == ""
}
