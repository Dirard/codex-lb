package domain

import "regexp"

// DefaultAntigravityClientVersion is the Antigravity client version presented
// in the User-Agent of Cloud Code calls until an administrator saves another.
const DefaultAntigravityClientVersion = "2.33.0"

var antigravityClientVersionPattern = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}$`)

// ValidAntigravityClientVersion accepts a dotted version such as 2.34.0.
func ValidAntigravityClientVersion(value string) bool {
	return antigravityClientVersionPattern.MatchString(value)
}
