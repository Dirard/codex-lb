package domain

import "regexp"

const DefaultCodexClientVersion = "0.156.0"

var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)

func ValidCodexClientVersion(value string) bool {
	return len(value) <= 64 && codexClientVersionPattern.MatchString(value)
}
