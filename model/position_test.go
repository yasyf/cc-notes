package model

import (
	"errors"
	"math/rand"
	"testing"
)

// TestMidPositionGoldens pins the raw midpoint algorithm. The "20" bound ends
// in '0', which no reachable position ever does (midpoints never trail '0'), so
// it exercises midPosition directly rather than through PositionBetween's
// validation gate.
func TestMidPositionGoldens(t *testing.T) {
	cases := []struct {
		before, after, want string
	}{
		{"", "", "i"},
		{"", "1", "0i"},
		{"a", "b", "ai"},
		{"a", "a1", "a0i"},
		{"1", "", "j"},
		{"1", "20", "2"},
		{"19", "2", "1n"},
	}
	for _, tc := range cases {
		t.Run(tc.before+"|"+tc.after, func(t *testing.T) {
			got := midPosition(tc.before, tc.after)
			if got != tc.want {
				t.Fatalf("midPosition(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.want)
			}
			if tc.before != "" && got <= tc.before {
				t.Fatalf("midPosition(%q, %q) = %q not after before", tc.before, tc.after, got)
			}
			if tc.after != "" && got >= tc.after {
				t.Fatalf("midPosition(%q, %q) = %q not before after", tc.before, tc.after, got)
			}
		})
	}
}

// TestPositionBetweenValidBounds pins PositionBetween over valid (reachable)
// bounds: the result is a valid position strictly between the two.
func TestPositionBetweenValidBounds(t *testing.T) {
	cases := []struct {
		before, after, want string
	}{
		{"", "", "i"},
		{"", "1", "0i"},
		{"a", "b", "ai"},
		{"a", "a1", "a0i"},
		{"1", "", "j"},
		{"19", "2", "1n"},
	}
	for _, tc := range cases {
		t.Run(tc.before+"|"+tc.after, func(t *testing.T) {
			got := PositionBetween(tc.before, tc.after)
			if got != tc.want {
				t.Fatalf("PositionBetween(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.want)
			}
			if err := validatePosition(got); err != nil {
				t.Fatalf("PositionBetween(%q, %q) = %q, invalid: %v", tc.before, tc.after, got, err)
			}
			if tc.before != "" && got <= tc.before {
				t.Fatalf("PositionBetween(%q, %q) = %q not after before", tc.before, tc.after, got)
			}
			if tc.after != "" && got >= tc.after {
				t.Fatalf("PositionBetween(%q, %q) = %q not before after", tc.before, tc.after, got)
			}
		})
	}
}

func TestPositionBetweenSqueezeLeft(t *testing.T) {
	before, after := "", ""
	for i := 0; i < 1000; i++ {
		mid := PositionBetween(before, after)
		if err := validatePosition(mid); err != nil {
			t.Fatalf("iter %d: invalid midpoint %q: %v", i, mid, err)
		}
		if before != "" && mid <= before {
			t.Fatalf("iter %d: before %q not < mid %q", i, before, mid)
		}
		if after != "" && mid >= after {
			t.Fatalf("iter %d: mid %q not < after %q", i, mid, after)
		}
		after = mid
	}
}

func TestPositionBetweenSqueezeRight(t *testing.T) {
	before, after := "", ""
	for i := 0; i < 1000; i++ {
		mid := PositionBetween(before, after)
		if err := validatePosition(mid); err != nil {
			t.Fatalf("iter %d: invalid midpoint %q: %v", i, mid, err)
		}
		if before != "" && mid <= before {
			t.Fatalf("iter %d: before %q not < mid %q", i, before, mid)
		}
		if after != "" && mid >= after {
			t.Fatalf("iter %d: mid %q not < after %q", i, mid, after)
		}
		before = mid
	}
}

func TestPositionBetweenSqueezeMiddle(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	before, after := "a", "b"
	for i := 0; i < 1000; i++ {
		mid := PositionBetween(before, after)
		if err := validatePosition(mid); err != nil {
			t.Fatalf("iter %d: invalid midpoint %q: %v", i, mid, err)
		}
		if mid <= before || mid >= after {
			t.Fatalf("iter %d: not before %q < mid %q < after %q", i, before, mid, after)
		}
		if rng.Intn(2) == 0 {
			before = mid
		} else {
			after = mid
		}
	}
}

func TestPositionBetweenPanics(t *testing.T) {
	cases := []struct {
		name          string
		before, after string
	}{
		{"before equals after", "a", "a"},
		{"before after after", "b", "a"},
		{"invalid before", "A", "z"},
		{"invalid after", "a", "z0"},
		{"empty before invalid after", "", "z0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("PositionBetween(%q, %q) did not panic", tc.before, tc.after)
				}
			}()
			PositionBetween(tc.before, tc.after)
		})
	}
}

func TestValidatePositionNegatives(t *testing.T) {
	for _, p := range []string{"", "0", "a0", "A", "a-b", "1G", "z0"} {
		if err := validatePosition(p); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("validatePosition(%q) = %v, want ErrInvalidValue", p, err)
		}
	}
}

func TestValidatePositionAccepts(t *testing.T) {
	for _, p := range []string{"i", "a", "z", "0i", "a0i", "1n", "abcz"} {
		if err := validatePosition(p); err != nil {
			t.Errorf("validatePosition(%q) = %v, want nil", p, err)
		}
	}
}
