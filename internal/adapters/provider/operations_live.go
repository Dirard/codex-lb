package provider

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// liveLifecycleEvents are the only sideband events a Live observer decodes;
// the audio reflections around them are skipped unread.
var liveLifecycleEvents = [][]byte{[]byte(`"session.started"`), []byte(`"session.usage.updated"`), []byte(`"session.closed"`)}

// ObserveLiveCall attaches a read-only sideband to a GPT-Live call on the
// account that created it and hands observe every upstream text frame that
// names a session start, usage or close event. It sends nothing but pings. A
// normal upstream close returns nil; a refused attachment is a
// ProviderFailure carrying the refusal status, so a 404 tells the caller the
// call is not active.
func (a *Adapter) ObserveLiveCall(ctx context.Context, target application.CodexOperationTarget, callID string, observe func([]byte)) error {
	if err := application.ValidateCodexRealtimeRequest(application.CodexRealtimeRequest{CallID: callID}); err != nil {
		return err
	}
	endpoint := liveBaseURL(a.config.ChatGPTBaseURL) + "/live/" + url.PathEscape(callID)
	upstream, refusal, err := a.dialRealtime(ctx, target, application.CodexRealtimeHeaders{Alpha: domain.LiveAlpha}, endpoint)
	if err != nil {
		var failure *application.ProviderFailure
		if errors.As(err, &failure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		status := http.StatusBadGateway
		if refusal >= 400 && refusal <= 599 {
			status = refusal
		}
		return providerFailure("live_observer_unavailable", status, false)
	}
	defer upstream.CloseNow()
	upstream.SetReadLimit(application.MaxRealtimeMessageBytes)
	readCtx, cancel := context.WithCancel(ctx)
	var lastUpstream atomic.Int64
	var delivering atomic.Bool // An observer delivers nothing downstream.
	lastUpstream.Store(time.Now().UnixNano())
	pingFailed := make(chan error, 1)
	pingerDone := make(chan struct{})
	go func() {
		defer close(pingerDone)
		probeRealtimeUpstream(readCtx, upstream, a.realtime, &lastUpstream, &delivering, pingFailed)
		if len(pingFailed) != 0 {
			_ = upstream.CloseNow() // Ends the blocked read below.
		}
	}()
	defer func() { cancel(); <-pingerDone }()
	var frame bytes.Buffer
	for {
		kind, reader, err := upstream.Reader(readCtx)
		if err == nil {
			frame.Reset()
			_, err = frame.ReadFrom(reader)
		}
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case len(pingFailed) == 0 && realtimeNormalClose(err):
				return nil
			default:
				return providerFailure("live_observer_disconnected", http.StatusBadGateway, true)
			}
		}
		lastUpstream.Store(time.Now().UnixNano())
		if kind == websocket.MessageText && liveLifecycleFrame(frame.Bytes()) {
			observe(frame.Bytes())
		}
	}
}

func liveLifecycleFrame(data []byte) bool {
	for _, event := range liveLifecycleEvents {
		if bytes.Contains(data, event) {
			return true
		}
	}
	return false
}

var _ application.CodexLiveCallObserver = (*Adapter)(nil)
