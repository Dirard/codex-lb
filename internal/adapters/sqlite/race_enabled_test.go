//go:build race

package sqlite

// raceDetector scales large synthetic fixtures down: the race detector also
// instruments the transpiled SQLite engine, which then runs about 40x slower.
const raceDetector = true

func init() {
	// One reprice job of 500 events can outlast the writer's 5 s execution
	// budget under the detector on a busy CI runner.
	repriceRawChunk = 100
}
