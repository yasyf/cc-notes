package kg

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Concept extraction is an identifier regex, not a model. Measured over 775
// records from 11 repositories on 2026-07-26 (cc-notes note 3628756), a
// GLiNER-class encoder cost 2001 s against the regex's 0.2 s and, on the only
// spans that create an edge — document frequency 2 or more — held 37 %
// precision against the regex's 67 %.
var (
	backtickSpan = regexp.MustCompile("`([^`\n]{2,60})`")
	camelSpan    = regexp.MustCompile(`\b(?:[A-Z][a-z0-9]+){2,}\b`)
	snakeSpan    = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
	kebabSpan    = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:-[a-z0-9]+)+\b`)
	dottedSpan   = regexp.MustCompile(`\b[a-zA-Z][\w-]*(?:\.[a-zA-Z][\w-]*)+\b`)
	pathySpan    = regexp.MustCompile(`\b[\w.-]+/[\w./-]+\b`)

	// bareVersion is the mandatory version filter: a release string carries no
	// topic, and it was what survived the encoder's own precision collapse.
	bareVersion = regexp.MustCompile(`^v?\d+(?:\.\d+)*(?:-[\w.]+)?$`)
	// punctuationOnly rejects a span with no letter to it — a date, a line
	// range, a bare ratio.
	punctuationOnly = regexp.MustCompile(`^[\d.\-_/]+$`)
)

const (
	// minConceptLen is the shortest span worth a node; below it a term is an
	// abbreviation shared by unrelated topics.
	minConceptLen = 3
	// maxBacktickWords caps a backticked span: a longer one is a quoted
	// sentence, not an identifier.
	maxBacktickWords = 4
	// conceptDFFloor is the smallest document frequency that creates an edge.
	// A term one entity mentions relates it to nothing, so it is not a node.
	conceptDFFloor = 2
	// conceptDFCapFraction caps a term's document frequency at this share of
	// the repository's entities: a term most entities mention discriminates
	// nothing. Measured 2026-07-27, 0.20 is both the loosest cap that still
	// suppresses a repository's own name (in cc-notes, "cc-notes" df=60 % and
	// "capt-hook" df=22 % carry 68 % of all concept pair mass) and the
	// tightest that costs no real signal (in monorepo it suppresses nothing;
	// 0.15 cuts the genuine topic term "test-sandsql" and 0.04 pair F1).
	conceptDFCapFraction = 0.20
)

// conceptStopwords are spans the shape rules match but that name no topic:
// English function words the dotted and kebab rules pick up, and the
// hyphenated adjectives that pervade this corpus's prose.
var conceptStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "from": true, "into": true, "not": true, "but": true,
	"e.g": true, "i.e": true, "etc": true, "vs": true, "n/a": true,
	"a/b": true, "to-do": true, "read-only": true, "write-only": true,
	"up-to-date": true, "so-far": true, "in-flight": true, "one-off": true,
	"end-to-end": true, "follow-up": true, "trade-off": true,
	"high-level": true, "low-level": true, "long-running": true,
	"well-known": true, "non-obvious": true, "self-hosted": true,
	"per-file": true, "per-repo": true, "per-run": true,
}

// concepts extracts the distinct identifier-shaped terms in text, sorted:
// backticked spans, CamelCase, snake_case, kebab-case, dotted paths, and
// slash paths. Backticked spans are read first and then blanked, so the shape
// rules never re-split what an agent already delimited.
func concepts(text string) []string {
	found := map[string]struct{}{}
	for _, m := range backtickSpan.FindAllStringSubmatch(text, -1) {
		span := strings.TrimSpace(m[1])
		if len(strings.Fields(span)) > maxBacktickWords {
			continue
		}
		keep(found, span)
	}
	body := backtickSpan.ReplaceAllString(text, " ")
	for _, re := range []*regexp.Regexp{camelSpan, snakeSpan, kebabSpan, dottedSpan, pathySpan} {
		for _, span := range re.FindAllString(body, -1) {
			keep(found, span)
		}
	}
	return slices.Sorted(maps.Keys(found))
}

// keep normalizes a span and records it unless a filter rejects it.
func keep(found map[string]struct{}, span string) {
	term := strings.ToLower(strings.Trim(strings.TrimSpace(span), "`\"'.,;:()[]{}"))
	if len(term) < minConceptLen || conceptStopwords[term] ||
		bareVersion.MatchString(term) || punctuationOnly.MatchString(term) {
		return
	}
	found[term] = struct{}{}
}

// conceptDFCap is the largest document frequency a concept may carry in a
// repository of n entities. The floor keeps a small repository from capping
// every term below the frequency that creates an edge at all.
func conceptDFCap(n int) int {
	return max(conceptDFFloor, int(conceptDFCapFraction*float64(n)))
}

// discriminating drops the terms that create no edge or too many: a term fewer
// than conceptDFFloor entities mention relates nothing, and one more than the
// cap relates everything.
func discriminating(perEntity map[NodeID][]string, entities int) map[string]int {
	df := map[string]int{}
	for _, terms := range perEntity {
		for _, term := range terms {
			df[term]++
		}
	}
	limit := conceptDFCap(entities)
	maps.DeleteFunc(df, func(_ string, n int) bool { return n < conceptDFFloor || n > limit })
	return df
}
