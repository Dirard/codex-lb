//go:build !race

package sqlite

// raceDetector scales large synthetic fixtures down under -race.
const raceDetector = false
