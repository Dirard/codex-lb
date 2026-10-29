//go:build !linux

package sqlite

// dropPageCache is unsupported here: cold benchmarks are skipped.
func dropPageCache(...string) (bool, error) { return false, nil }
