package application

import (
	"errors"
	"time"
)

type responseOutput struct {
	emit         func(ResponseEvent) error
	prelude      []ResponseEvent
	preludeBytes int
	terminal     *ResponseEvent
	visible      bool
	started      time.Time
	firstEventMS int64
	firstTokenMS int64
}

func newResponseOutput(emit func(ResponseEvent) error) *responseOutput {
	return &responseOutput{emit: emit, started: time.Now()}
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
			if len(o.prelude) >= 8 || o.preludeBytes+len(event.Data) > 64<<10 {
				return errors.New("response prelude exceeds limit")
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
	for _, event := range o.prelude {
		o.visible = true
		if err := o.emit(event); err != nil {
			return err
		}
	}
	o.prelude = nil
	return nil
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
