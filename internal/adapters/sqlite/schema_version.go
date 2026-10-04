package sqlite

// SchemaVersion reports the exact schema supported by this executable without
// opening a database or applying migrations during updater preflight.
func SchemaVersion() int { return len(migrations) }
