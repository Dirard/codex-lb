package upstream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

const (
	maxSSEEventBytes = 16 << 20
	maxSSEEvents     = 100000
)

// ReadSSE shares the bounded parser with native provider endpoints. A callback
// error stops reading immediately, including an endpoint's terminal sentinel.
func ReadSSE(reader io.Reader, handle func(event string, data []byte) error) error {
	return readSSE(reader, handle)
}

func readSSE(reader io.Reader, handle func(event string, data []byte) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxSSEEventBytes)
	var data bytes.Buffer
	eventName := ""
	size := 0
	dispatch := func() error {
		if data.Len() == 0 && eventName == "" {
			return nil
		}
		payload := append([]byte(nil), data.Bytes()...)
		if len(payload) > 0 && payload[len(payload)-1] == '\n' {
			payload = payload[:len(payload)-1]
		}
		err := handle(eventName, payload)
		data.Reset()
		eventName = ""
		return err
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		size += len(line) + 1
		if size > maxSSEEventBytes {
			return &Error{Code: ErrorCodeInvalidStream, Message: "upstream SSE event exceeds bounded size"}
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
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.Write(value)
			case "event":
				eventName = string(value)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return &Error{Code: ErrorCodeInvalidStream, Message: "upstream SSE line exceeds bounded size"}
		}
		return &Error{Code: ErrorCodeConnection, Message: "upstream SSE read failed"}
	}
	if data.Len() > 0 || eventName != "" {
		return dispatch()
	}
	return nil
}
