package domain

import "strings"

// ParseRuntimeVersion parses strict stable runtime tags of the form
// go-vMAJOR.MINOR.PATCH. Legacy tags, prereleases, non-canonical numbers and
// components outside the uint64 range are rejected.
func ParseRuntimeVersion(version string) ([3]uint64, bool) {
	var parsed [3]uint64
	rest, ok := strings.CutPrefix(version, "go-v")
	if !ok {
		return parsed, false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != len(parsed) {
		return parsed, false
	}
	for i, part := range parts {
		value, ok := parseVersionComponent(part)
		if !ok {
			return parsed, false
		}
		parsed[i] = value
	}
	return parsed, true
}

// NewerRuntimeVersion reports whether left is strictly newer than right.
// Invalid versions fail closed and never compare as newer.
func NewerRuntimeVersion(left, right string) bool {
	leftVersion, ok := ParseRuntimeVersion(left)
	if !ok {
		return false
	}
	rightVersion, ok := ParseRuntimeVersion(right)
	if !ok {
		return false
	}
	for i := range leftVersion {
		if leftVersion[i] != rightVersion[i] {
			return leftVersion[i] > rightVersion[i]
		}
	}
	return false
}

func parseVersionComponent(part string) (uint64, bool) {
	if part == "" || (len(part) > 1 && part[0] == '0') {
		return 0, false
	}
	var value uint64
	for i := 0; i < len(part); i++ {
		digit := part[i] - '0'
		if digit > 9 {
			return 0, false
		}
		if value > (^uint64(0)-uint64(digit))/10 {
			return 0, false
		}
		value = value*10 + uint64(digit)
	}
	return value, true
}
