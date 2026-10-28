package sqlite

// SchemaVersion reports the exact schema supported by this executable without
// opening a database or applying migrations during updater preflight.
func SchemaVersion() int { return len(migrations) }

// SchemaFloor reports the lowest code schema that may operate on a database at
// SchemaVersion: the newest migration that is not additive. Migrating records
// it in the database, so older code can tell whether it may open a newer one.
func SchemaFloor() int {
	floor := len(migrations)
	for floor > 0 && additiveMigrations[floor] {
		floor--
	}
	return floor
}
