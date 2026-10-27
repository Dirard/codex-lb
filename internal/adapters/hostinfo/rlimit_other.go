//go:build !unix

package hostinfo

func fileDescriptorLimit() int64 { return 0 }
