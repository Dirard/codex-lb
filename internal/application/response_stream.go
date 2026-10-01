package application

import (
	"errors"
	"time"
)

var ErrResponsePreludeStorage = &ProxyError{Code: "response_prelude_storage_failed", Status: 503, Message: "Could not buffer the upstream response; do not blindly repeat the request"}

type responseOutput struct {
	emit         func(ResponseEvent) error
	prelude      []ResponseEvent
	preludeBytes int
	preludeCount int
	openPrelude  func() (ResponsePrelude, error)
	spilled      ResponsePrelude
	terminal     *ResponseEvent
	visible      bool
	started      time.Time
	firstEventMS int64
	firstTokenMS int64
}

func newResponseOutput(emit func(ResponseEvent) error, openPrelude func() (ResponsePrelude, error)) *responseOutput {
	return &responseOutput{emit: emit, openPrelude: openPrelude, started: time.Now()}
}

func (o *responseOutput) send(event ResponseEvent) error {
	if o.emit == nil {
		return errors.New("unexpected stream event for non-streaming request")
	}
	if o.terminal != nil {
		return errors.New("event after terminal response")
	}
	if o.firstEventMS == 0 {
		o.firstEventMS = max(1, time.Since(o.started).Milliseconds())
	}
	if o.firstTokenMS == 0 && (event.Type == "response.output_text.delta" || event.Type == "response.reasoning_text.delta" || event.Type == "response.reasoning_summary_text.delta") {
		o.firstTokenMS = max(1, time.Since(o.started).Milliseconds())
	}
	switch event.Type {
	case "response.completed", "response.failed", "response.incomplete":
		o.terminal = &event
		return nil
	case "response.created", "response.in_progress":
		if !o.visible {
			if o.preludeCount >= MaxResponsePreludeEvents || len(event.Data) > MaxResponsePreludeEventBytes {
				return &ProxyError{Code: "response_prelude_limit_exceeded", Status: 502, Message: "Upstream response startup exceeds the supported event limit"}
			}
			o.preludeCount++
			if o.spilled != nil || o.preludeBytes+len(event.Data) > 64<<10 {
				return o.spill(event)
			}
			o.prelude = append(o.prelude, event)
			o.preludeBytes += len(event.Data)
			return nil
		}
	}
	if err := o.flush(); err != nil {
		return err
	}
	o.visible = true
	return o.emit(event)
}

func (o *responseOutput) flush() error {
	if o.spilled != nil {
		defer o.close()
		return o.spilled.Replay(func(event ResponseEvent) error {
			o.visible = true
			return o.emit(event)
		})
	}
	for _, event := range o.prelude {
		o.visible = true
		if err := o.emit(event); err != nil {
			return err
		}
	}
	o.prelude = nil
	o.preludeBytes = 0
	return nil
}

// spill preserves the complete prelude without retaining large echoed requests in memory.
func (o *responseOutput) spill(event ResponseEvent) error {
	if o.spilled == nil {
		if o.openPrelude == nil {
			return ErrResponsePreludeStorage
		}
		var err error
		o.spilled, err = o.openPrelude()
		if err != nil || o.spilled == nil {
			return ErrResponsePreludeStorage
		}
		for _, buffered := range o.prelude {
			if err := o.spilled.Append(buffered); err != nil {
				return ErrResponsePreludeStorage
			}
		}
		o.prelude, o.preludeBytes = nil, 0
	}
	if err := o.spilled.Append(event); err != nil {
		return ErrResponsePreludeStorage
	}
	return nil
}

func (o *responseOutput) close() {
	if o == nil {
		return
	}
	if o.spilled != nil {
		_ = o.spilled.Close()
		o.spilled = nil
	}
	o.prelude, o.preludeBytes = nil, 0
}

func (o *responseOutput) finish() error {
	if o.terminal == nil {
		return errors.New("missing terminal event")
	}
	if err := o.flush(); err != nil {
		return err
	}
	o.visible = true
	return o.emit(*o.terminal)
}
