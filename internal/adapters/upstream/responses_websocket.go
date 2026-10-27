package upstream

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func (a *HTTPAdapter) streamResponsesWebSocket(ctx context.Context, target Target, req responsesWireRequest, catalog toolCatalog, emit func(Event) error) (Result, error) {
	_ = catalog
	body, err := passthroughBody(req, target.Capabilities)
	if err != nil {
		return Result{}, err
	}
	create, err := websocketResponseCreate(body)
	if err != nil {
		return Result{}, err
	}

	session, rejected, err := a.sessions.acquire(ctx, target, req.PreviousResponseID, a.client, websocketReuseFence{a.websocketReuseProbeAfter, a.websocketReuseProbeTimeout})
	if err != nil {
		return rejected, err
	}
	keep, responseID := false, ""
	defer func() { a.sessions.release(session, keep, responseID, true) }()
	a.sessions.mu.Lock()
	priorID := session.latest
	a.sessions.mu.Unlock()
	connection := session.connection
	lease := session.arm()
	defer session.disarm(lease)

	writeCtx, stopWrite := context.WithTimeout(ctx, websocketWriteTimeout)
	err = connection.Write(writeCtx, websocket.MessageText, create)
	stopWrite()
	if err != nil {
		return Result{}, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: err.Error()})
	}
	probeErrors, stopProbe := a.probeWebSocket(ctx, session)
	defer stopProbe()
	var interrupts chan string
	if target.OnInterruptReady != nil {
		interrupts = make(chan string, 1)
		target.OnInterruptReady(interrupts)
		defer target.OnInterruptReady(nil)
	}
	stopInterrupts := func() error {
		if interrupts == nil {
			return nil
		}
		// Unregister under the ingress lock before draining, so no sender can race release.
		target.OnInterruptReady(nil)
		queue := interrupts
		interrupts = nil
		select {
		case id := <-queue:
			return emit(interruptFailureEvent(id, "response_already_ended", "The response ended before interruption was sent"))
		default:
			return nil
		}
	}
	currentID, sentInterrupt, pendingInterrupt := "", "", ""
	var terminal *Event
	var result Result
	var ackTimer *time.Timer
	var ackTimeout <-chan time.Time
	defer func() {
		if ackTimer != nil {
			ackTimer.Stop()
		}
	}()
	finish := func(reuse bool) (Result, error) {
		if err := emit(*terminal); err != nil {
			return result, err
		}
		keep, responseID = reuse, result.ResponseID
		return result, nil
	}
	missingAck := func() (Result, error) {
		if err := emit(interruptFailureEvent(pendingInterrupt, "interrupt_acknowledgement_missing", "The response ended without an interruption acknowledgement")); err != nil {
			return result, err
		}
		return finish(false)
	}

	received := 0
	for {
		var frame websocketFrame
		select {
		case id := <-interrupts:
			if currentID == "" || id != currentID || sentInterrupt != "" {
				if err := emit(interruptFailureEvent(id, "response_interrupt_unavailable", "The response cannot accept this interruption")); err != nil {
					return result, err
				}
				continue
			}
			writeCtx, stopWrite := context.WithTimeout(ctx, websocketWriteTimeout)
			err := connection.Write(writeCtx, websocket.MessageText, mustJSON(struct {
				Type       string `json:"type"`
				ResponseID string `json:"response_id"`
				Mode       string `json:"mode"`
			}{"response.interrupt", id, "discard_partial_items"}))
			stopWrite()
			if err != nil {
				return result, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: "Could not send response interruption"})
			}
			sentInterrupt, pendingInterrupt = id, id
			continue
		case frame = <-lease.frames:
		case <-session.readerDone:
			// The unbuffered hand-off delivered every frame the reader read.
			if terminal != nil {
				return missingAck()
			}
			return result, &Error{Code: ErrorCodeStreamIncomplete, Message: "Upstream WebSocket closed before a terminal event"}
		case <-ctx.Done():
			return result, ctx.Err()
		case <-ackTimeout:
			return missingAck()
		case err := <-probeErrors:
			if terminal != nil {
				return missingAck()
			}
			return result, wrapContext(ctx, err)
		}
		data, err := frame.data, frame.err
		if err != nil {
			if terminal != nil {
				return missingAck()
			}
			if websocket.CloseStatus(err) != -1 {
				err = &Error{Code: ErrorCodeStreamIncomplete, Message: "upstream WebSocket closed before a terminal event"}
			}
			return result, wrapContext(ctx, err)
		}
		if frame.kind != websocket.MessageText {
			return result, &Error{Code: ErrorCodeInvalidStream, Message: "Expected a text WebSocket event"}
		}
		if len(data) > maxSSEEventBytes {
			return result, &Error{Code: ErrorCodeInvalidStream, Message: "upstream WebSocket event exceeds bounded size"}
		}
		if received += len(data); received > maxStreamBytes {
			return result, streamTooLarge()
		}
		// conn.Read returns a fresh buffer per frame; events forward it without copying.
		header, err := decodeEventHeader(data)
		if err != nil {
			return result, err
		}
		eventType := header.Type
		if eventType == "" {
			return result, &Error{Code: ErrorCodeInvalidStream, Message: "upstream WebSocket event omitted its type"}
		}
		if eventType == "response.interrupt.accepted" || eventType == "response.interrupt.failed" {
			var ack struct {
				ResponseID *string `json:"response_id"`
			}
			if json.Unmarshal(data, &ack) != nil || ack.ResponseID == nil || pendingInterrupt == "" || *ack.ResponseID != pendingInterrupt {
				return result, &Error{Code: ErrorCodeInvalidStream, Message: "Unexpected response interruption acknowledgement"}
			}
			pendingInterrupt = ""
			if err := emit(Event{Type: eventType, Data: data}); err != nil {
				return result, err
			}
			if terminal != nil {
				return finish(true)
			}
			continue
		}
		if eventType == "error" || header.Error != nil {
			if terminal != nil {
				return missingAck()
			}
			var envelope struct {
				Status int `json:"status"`
			}
			_ = json.Unmarshal(data, &envelope)
			result, _ := rejectedResult(envelope.Status, data, target.Capabilities.Protocol)
			return result, responseEventError(data)
		}
		switch eventType {
		case "response.created", "response.in_progress":
			var announced struct {
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			_ = json.Unmarshal(data, &announced)
			response := announced.Response
			if response.ID != "" && (response.ID == priorID || currentID != "" && response.ID != currentID) {
				return result, &Error{Code: ErrorCodeInvalidStream, Message: "Upstream event belongs to another response"}
			}
			if eventType == "response.created" {
				currentID = response.ID
			}
		case "response.completed", "response.failed", "response.incomplete":
			if terminal != nil {
				return result, &Error{Code: ErrorCodeInvalidStream, Message: "Upstream sent a second terminal response while awaiting interruption acknowledgement"}
			}
			terminalResult, err := terminalFromResponsesEvent(eventType, data, target.Capabilities.AllowMissingUsage)
			if err != nil {
				return terminalResult, err
			}
			// A terminal for another response must not settle this request.
			if terminalResult.ResponseID == priorID || currentID != "" && terminalResult.ResponseID != currentID {
				return result, &Error{Code: ErrorCodeInvalidStream, Message: "Upstream terminal belongs to another response"}
			}
			result = terminalResult
			if sentInterrupt != "" && result.ResponseID != sentInterrupt {
				return result, &Error{Code: ErrorCodeInvalidStream, Message: "Interruption terminal belongs to another response"}
			}
			if err := stopInterrupts(); err != nil {
				return result, err
			}
			terminal = &Event{Type: eventType, Data: data}
			if pendingInterrupt != "" {
				ackTimer = time.NewTimer(5 * time.Second)
				ackTimeout = ackTimer.C
				continue
			}
			return finish(true)
		}
		if err := emit(Event{Type: eventType, Data: data}); err != nil {
			return result, err
		}
	}
}

// websocketWriteTimeout bounds one upstream WebSocket write, matching the
// downstream write bound so neither side can hold a lease indefinitely.
const websocketWriteTimeout = 30 * time.Second

func interruptFailureEvent(responseID, code, message string) Event {
	return Event{Type: "response.interrupt.failed", Data: mustJSON(struct {
		Type       string            `json:"type"`
		ResponseID string            `json:"response_id"`
		Error      map[string]string `json:"error"`
	}{"response.interrupt.failed", responseID, map[string]string{"code": code, "message": message}})}
}

func websocketResponseCreate(body json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, &Error{Code: ErrorCodeInvalidConfiguration, Message: "Responses request was not a JSON object"}
	}
	var background bool
	if raw, ok := object["background"]; ok && (json.Unmarshal(raw, &background) != nil || background) {
		return nil, &Error{Code: ErrorCodeUnsupportedCapability, Message: "Background Responses are not supported over WebSocket"}
	}
	delete(object, "background")
	delete(object, "stream")
	object["type"] = mustJSON("response.create")
	return mustJSON(object), nil
}

func websocketURL(httpEndpoint string) string {
	if strings.HasPrefix(httpEndpoint, "https://") {
		return "wss://" + strings.TrimPrefix(httpEndpoint, "https://")
	}
	if strings.HasPrefix(httpEndpoint, "http://") {
		return "ws://" + strings.TrimPrefix(httpEndpoint, "http://")
	}
	return httpEndpoint
}
