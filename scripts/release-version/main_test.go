package main

import "testing"

func TestCompare(t *testing.T) {
	for _, test := range []struct {
		name    string
		target  string
		current string
		want    int
	}{
		{"numeric patch", "1.0.10", "1.0.9", 1},
		{"older minor", "1.9.99", "1.10.0", -1},
		{"new major", "2.0.0", "1.99.99", 1},
		{"same version", "1.2.3", "1.2.3", 0},
		{"release after RC", "1.2.3", "1.2.3-rc.10", 1},
		{"RC before release", "1.2.3-rc.10", "1.2.3", -1},
		{"numeric RC", "1.2.3-rc.10", "1.2.3-rc.2", 1},
		{"prerelease next minor", "1.3.0-alpha.1", "1.2.99", 1},
		{"build precedence", "1.2.3+next", "1.2.3+previous", 0},
		{"RC build precedence", "1.2.3-rc.2+next", "1.2.3-rc.2+previous", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := compare(test.target, test.current)
			if err != nil || got != test.want {
				t.Fatalf("compare(%q, %q) = %d, %v; want %d", test.target, test.current, got, err, test.want)
			}
		})
	}
}

func TestCompareRejectsInvalidVersions(t *testing.T) {
	for _, invalid := range []string{"", "1", "1.2", "01.2.3", "1.2.3-rc.01", "1.2.3+", "dev", "v1.2.3"} {
		t.Run(invalid, func(t *testing.T) {
			if _, err := compare(invalid, "1.2.3"); err == nil {
				t.Fatalf("accepted target %q", invalid)
			}
			if _, err := compare("1.2.3", invalid); err == nil {
				t.Fatalf("accepted current %q", invalid)
			}
		})
	}
}
