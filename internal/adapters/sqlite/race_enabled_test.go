//go:build race

package sqlite

// raceDetector scales large synthetic fixtures down: the race detector also
// instruments the transpiled SQLite engine, which then runs about 40x slower.
const raceDetector = true
