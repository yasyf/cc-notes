package model

import (
	"fmt"
	"strings"
)

// positionDigits is the base-36 alphabet step positions are written in. Byte
// order equals digit order, so positions compare with plain string comparison.
const positionDigits = "0123456789abcdefghijklmnopqrstuvwxyz"

// PositionBetween returns a step position strictly between before and after. An
// empty before means the start of the position space and an empty after the
// end, so PositionBetween("", "") seeds the first step and
// PositionBetween(last, "") appends. Both bounds must be valid positions with
// before strictly less than after; a violation is programmer error and panics.
// A result always exists — positions are fractions in base 36 with no trailing
// zero digit — so inserting between adjacent steps never renumbers neighbors.
func PositionBetween(before, after string) string {
	if before != "" {
		mustValidPosition(before)
	}
	if after != "" {
		mustValidPosition(after)
	}
	if before != "" && after != "" && before >= after {
		panic(fmt.Sprintf("model: position %q not before %q", before, after))
	}
	return midPosition(before, after)
}

func midPosition(a, b string) string {
	if b != "" {
		n := 0
		for n < len(b) && positionDigitAt(a, n) == b[n] {
			n++
		}
		if n > 0 {
			return b[:n] + midPosition(positionTail(a, n), b[n:])
		}
	}
	da := 0
	if a != "" {
		da = strings.IndexByte(positionDigits, a[0])
	}
	db := len(positionDigits)
	if b != "" {
		db = strings.IndexByte(positionDigits, b[0])
	}
	if db-da > 1 {
		return string(positionDigits[(da+db+1)/2])
	}
	if len(b) > 1 {
		return b[:1]
	}
	return string(positionDigits[da]) + midPosition(positionTail(a, 1), "")
}

func positionDigitAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return positionDigits[0]
}

func positionTail(s string, n int) string {
	if n < len(s) {
		return s[n:]
	}
	return ""
}

func validatePosition(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty step position", ErrInvalidValue)
	}
	for i := range len(p) {
		if strings.IndexByte(positionDigits, p[i]) < 0 {
			return fmt.Errorf("%w: step position %q", ErrInvalidValue, p)
		}
	}
	if p[len(p)-1] == positionDigits[0] {
		return fmt.Errorf("%w: step position %q ends in %q", ErrInvalidValue, p, positionDigits[0])
	}
	return nil
}

func mustValidPosition(p string) {
	if err := validatePosition(p); err != nil {
		panic("model: " + err.Error())
	}
}
