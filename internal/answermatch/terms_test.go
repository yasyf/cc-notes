package answermatch

import (
	"maps"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

var (
	matchOracleWord      = regexp.MustCompile(`[a-z0-9]+(?:[.-][a-z0-9]+)*`)
	matchOracleLetterNum = regexp.MustCompile(`([a-z])([0-9])`)
	matchOracleNumLetter = regexp.MustCompile(`([0-9])([a-z])`)
	matchOracleJoiner    = regexp.MustCompile(`[.-]`)
)

func regexMatchTerms(text string) map[string]struct{} {
	spaced := matchOracleNumLetter.ReplaceAllString(matchOracleLetterNum.ReplaceAllString(strings.ToLower(text), "$1 $2"), "$1 $2")
	terms := map[string]struct{}{}
	add := func(word string) {
		_, stop := matchStopWords[word]
		if stop || len(word) < matchMinTerm || isDigits(word) && len(word) != matchStatusLen {
			return
		}
		if len(word) > matchMinTerm && strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") {
			word = strings.TrimSuffix(word, "s")
		}
		terms[word] = struct{}{}
	}
	for _, word := range matchOracleWord.FindAllString(spaced, -1) {
		add(word)
		for _, part := range matchOracleJoiner.Split(word, -1) {
			add(part)
		}
	}
	return terms
}

func TestMatchTermsScannerMatchesRegex(t *testing.T) {
	check := func(text string) {
		t.Helper()
		got, want := Terms(text), regexMatchTerms(text)
		if !maps.Equal(got, want) {
			t.Fatalf("text=%q got=%v want=%v", text, got, want)
		}
	}
	for _, text := range []string{
		"", "edge403s", "a1b2c3", "1a2b3c", "a.1b-2c", "a..b--c.-403s",
		"GSK edge-403 HTTP500errors 123 12 1234 tests class grass the THE",
		"Kelvin İSTANBUL Straße café αEDGE403β 🐋 ẞABC123def",
		".foo-123.bar456baz-789..", "http://foo.example.com/v1/cache?code=403",
	} {
		check(text)
	}
	random := rand.New(rand.NewPCG(31, 47))
	alphabet := []byte("abcxyzABCXYZ0123456789.-_ !/\n\x00\xff")
	for range 2000 {
		data := make([]byte, random.IntN(256))
		for i := range data {
			data[i] = alphabet[random.IntN(len(alphabet))]
		}
		check(string(data))
	}
}

func FuzzMatchTermsScanner(f *testing.F) {
	for _, text := range []string{"edge403s", "a1b2c3", "Kelvin-403.example", "a..b--c"} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		got, want := Terms(text), regexMatchTerms(text)
		if !maps.Equal(got, want) {
			t.Fatalf("text=%q got=%v want=%v", text, got, want)
		}
	})
}

func TestMatchTermsScannerAllocationCost(t *testing.T) {
	text := strings.Repeat("GSK edge403s cache-writes status.500 calls /data/v1\n", 200)
	want := regexMatchTerms(text)
	var found map[string]struct{}
	oracle := testing.AllocsPerRun(5, func() { found = regexMatchTerms(text) })
	scanner := testing.AllocsPerRun(5, func() { found = Terms(text) })
	if !maps.Equal(found, want) {
		t.Fatal("cost fixture differs from regex output")
	}
	t.Logf("regex_allocations=%.0f scanner_allocations=%.0f", oracle, scanner)
	if scanner*4 > oracle {
		t.Fatalf("scanner allocations %.0f exceed one quarter of regex %.0f", scanner, oracle)
	}
}

func BenchmarkMatchTermsScanner(b *testing.B) {
	text := strings.Repeat("GSK edge403s cache-writes status.500 calls /data/v1\n", 200)
	for _, candidate := range []struct {
		name string
		run  func(string) map[string]struct{}
	}{{"regex", regexMatchTerms}, {"scanner", Terms}} {
		b.Run(candidate.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if len(candidate.run(text)) == 0 {
					b.Fatal("empty token set")
				}
			}
		})
	}
}
