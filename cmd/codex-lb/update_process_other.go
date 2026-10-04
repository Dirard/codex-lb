//go:build !linux

package main

import "syscall"

func updateProcessAttributes() *syscall.SysProcAttr { return nil }
