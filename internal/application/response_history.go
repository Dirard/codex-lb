package application

// lazyHistory decrypts and parses a continuation owner's stored history at most
// once. Respond loads it before dispatch only when the owner already refused
// quota (the replay needs it); otherwise it starts the work beside the upstream
// call and reads it only for a replay or the next continuation. The goroutine
// is owned by Respond, which always joins it before returning. A nil history
// means the owner stored none.
type lazyHistory struct {
	load    func() (responseContext, error)
	started bool
	done    chan struct{}
	value   responseContext
	err     error
}

// start begins loading in a request-owned goroutine; repeated calls are no-ops.
func (h *lazyHistory) start() {
	if h == nil || h.started {
		return
	}
	h.started = true
	h.done = make(chan struct{})
	go func() {
		defer close(h.done)
		defer func() {
			if recover() != nil {
				h.value, h.err = responseContext{}, errInvalidStoredContinuation
			}
		}()
		h.value, h.err = h.load()
	}()
}

// get returns the parsed history, loading it now if it was not started.
func (h *lazyHistory) get() (responseContext, error) {
	if h == nil {
		return responseContext{}, nil
	}
	h.start()
	<-h.done
	return h.value, h.err
}

// join waits for a started load; Respond defers it on every return path.
func (h *lazyHistory) join() {
	if h != nil && h.started {
		<-h.done
	}
}
