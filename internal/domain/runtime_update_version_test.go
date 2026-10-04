package domain

import "testing"

func TestParseRuntimeVersion(t *testing.T) {
	valid := map[string][3]uint64{
		"go-v0.0.0":                    {0, 0, 0},
		"go-v1.2.3":                    {1, 2, 3},
		"go-v10.20.30":                 {10, 20, 30},
		"go-v18446744073709551615.0.1": {^uint64(0), 0, 1},
	}
	for version, expected := range valid {
		parsed, ok := ParseRuntimeVersion(version)
		if !ok || parsed != expected {
			t.Fatalf("ParseRuntimeVersion(%q) = %v, %v; want %v, true", version, parsed, ok, expected)
		}
	}
	invalid := []string{
		"",
		"v1.2.3",
		"go-1.2.3",
		"go-v1.2",
		"go-v1.2.3.4",
		"go-v1.2.3-beta",
		"go-v1.2.3+build",
		"go-v01.2.3",
		"go-v1.02.3",
		"go-v1.2.03",
		"go-v1..3",
		"go-v.2.3",
		"go-v1.2.x",
		"go-v1.2.3 ",
		" go-v1.2.3",
		"GO-V1.2.3",
		"go-v18446744073709551616.0.0",
		"go-v1.2.-3",
		"go-v+1.2.3",
		"go-v1,2.3",
		"go-v1.2.3\n",
		"go-v1_2_3",
	}
	for _, version := range invalid {
		if parsed, ok := ParseRuntimeVersion(version); ok {
			t.Fatalf("ParseRuntimeVersion(%q) = %v; want rejection", version, parsed)
		}
	}
}

func TestNewerRuntimeVersion(t *testing.T) {
	cases := []struct {
		left  string
		right string
		want  bool
	}{
		{"go-v1.2.3", "go-v1.2.2", true},
		{"go-v1.3.0", "go-v1.2.99", true},
		{"go-v2.0.0", "go-v1.999.999", true},
		{"go-v1.2.3", "go-v1.2.3", false},
		{"go-v1.2.2", "go-v1.2.3", false},
		{"go-v1.2.3", "invalid", false},
		{"invalid", "go-v1.2.3", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := NewerRuntimeVersion(tc.left, tc.right); got != tc.want {
			t.Fatalf("NewerRuntimeVersion(%q, %q) = %v; want %v", tc.left, tc.right, got, tc.want)
		}
	}
}
