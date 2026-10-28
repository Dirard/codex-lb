package main

import (
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// workerHandoff is what a launcher lends a handoff worker: the shared listener,
// the installation's database lock, the lifeline and the worker's identity.
type workerHandoff struct {
	listener net.Listener
	lock     *os.File
	lifeline *os.File
	instance string
	peers    []string
	overlap  bool
}

func validInstance(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && strings.ToLower(value) == value
}

// inheritedHandoff returns nil for a worker started by a launcher that binds
// nothing for it (a released launcher): that worker binds and locks itself.
func inheritedHandoff(dataDir string) (*workerHandoff, error) {
	if os.Getenv(handoffEnv) != "1" || handoffSupport != "on" {
		return nil, nil
	}
	instance := os.Getenv(instanceEnv)
	if !validInstance(instance) {
		return nil, errors.New("invalid managed worker identity")
	}
	var peers []string
	for _, peer := range strings.Split(os.Getenv(peersEnv), ",") {
		if peer == "" {
			continue
		}
		if !validInstance(peer) {
			return nil, errors.New("invalid managed worker identity")
		}
		peers = append(peers, peer)
	}
	listenerFile := os.NewFile(listenerDescriptor, "managed-listener")
	lock := os.NewFile(databaseLockDescriptor, "database-lock")
	lifeline := os.NewFile(lifelineDescriptor, "lifeline")
	if listenerFile == nil || lock == nil || lifeline == nil {
		return nil, errors.New("managed worker descriptors are missing")
	}
	listener, err := net.FileListener(listenerFile)
	listenerFile.Close()
	if err != nil {
		lock.Close()
		lifeline.Close()
		return nil, errors.New("inherited managed listener is invalid")
	}
	if err := verifyDatabaseLock(lock, filepath.Join(dataDir, "codex-lb.sqlite3")); err != nil {
		listener.Close()
		lock.Close()
		lifeline.Close()
		return nil, err
	}
	return &workerHandoff{listener: listener, lock: lock, lifeline: lifeline, instance: instance, peers: peers, overlap: os.Getenv(overlapEnv) == "1"}, nil
}

// watchLifeline calls stop once the launcher is gone, as SIGTERM would.
func (h *workerHandoff) watchLifeline(stop func()) {
	go func() {
		_, _ = io.Copy(io.Discard, h.lifeline)
		stop()
	}()
}
