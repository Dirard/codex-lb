package upstream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

const (
	maxSSEEventBytes = 16 << 20
	// maxStreamBytes bounds the total event payload of one upstream response.
	// Streams are bounded by size and the overall response deadline, not by
	// an event count that a long healthy response can exceed.
	maxStreamBytes = 256 << 20
)

func streamTooLarge() error {
	return &Error{Code: ErrorCodeStreamIncomplete, Message: "upstream stream exceeds bounded size"}
}

// ReadSSE shares the bounded parser with native provider endpoints. A callback
// error stops reading immediately, including an endpoint's terminal sentinel.
func ReadSSE(reader io.Reader, handle func(event string, data []byte) error) error {
	return readSSE(reader, handle)
}

// readSSE hands every event a buffer it owns (one allocation per event), so
// handlers may retain data without copying it.
func readSSE(reader io.Reader, handle func(event string, data []byte) error) error {
	scanner := bufio.NewScanner(reader)
	// The scanner grows on demand up to the event bound; most lines are small.
	scanner.Buffer(make([]byte, 8<<10), maxSSEEventBytes)
	var data []byte
	eventName, lastName := "", ""
	size, total := 0, 0
	dispatch := func() error {
		if len(data) == 0 && eventName == "" {
			return nil
		}
		payload := data
		if len(payload) > 0 && payload[len(payload)-1] == '\n' {
			payload = payload[:len(payload)-1]
		}
		err := handle(eventName, payload)
		data = nil
		eventName = ""
		return err
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		size += len(line) + 1
		if size > maxSSEEventBytes {
			return &Error{Code: ErrorCodeInvalidStream, Message: "upstream SSE event exceeds bounded size"}
		}
		if total += len(line) + 1; total > maxStreamBytes {
			return streamTooLarge()
		}
		{
			trimmed := line
			if len(trimmed) == 0 {
				if err := dispatch(); err != nil {
					return err
				}
				size = 0
				continue
			}
			if trimmed[0] == ':' {
				continue
			}
			field, value, hasValue := bytes.Cut(trimmed, []byte(":"))
			if hasValue && len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			switch string(field) {
			case "data":
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, value...)
			case "event":
				// Consecutive events usually share a name; reuse its string.
				if string(value) != lastName {
					lastName = string(value)
				}
				eventName = lastName
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return &Error{Code: ErrorCodeInvalidStream, Message: "upstream SSE line exceeds bounded size"}
		}
		return &Error{Code: ErrorCodeConnection, Message: "upstream SSE read failed"}
	}
	if len(data) > 0 || eventName != "" {
		return dispatch()
	}
	return nil
}
