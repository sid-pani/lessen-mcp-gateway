package transport

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type SSEEvent struct {
	ID    string
	Event string
	Data  []byte
	Retry int
}

func ReadSSE(reader *bufio.Reader, maxBytes int64) (SSEEvent, error) {
	var event SSEEvent
	var data []string
	var consumed int64
	for {
		line, err := reader.ReadString('\n')
		consumed += int64(len(line))
		if maxBytes > 0 && consumed > maxBytes {
			return SSEEvent{}, errors.New("SSE event exceeds configured response limit")
		}
		if err != nil && len(line) == 0 {
			return SSEEvent{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			event.Data = []byte(strings.Join(data, "\n"))
			return event, nil
		}
		if strings.HasPrefix(line, ":") {
			if err != nil {
				return SSEEvent{}, err
			}
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field = line
			value = ""
		} else {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			event.Event = value
		case "data":
			data = append(data, value)
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				event.ID = value
			}
		case "retry":
			_, _ = fmt.Sscanf(value, "%d", &event.Retry)
		}
		if err != nil {
			return SSEEvent{}, err
		}
	}
}

func DecodeSSEResponse(event SSEEvent) ([]byte, error) {
	if len(event.Data) == 0 {
		return nil, io.EOF
	}
	var raw json.RawMessage
	if err := json.Unmarshal(event.Data, &raw); err != nil {
		return nil, fmt.Errorf("decode SSE JSON: %w", err)
	}
	return event.Data, nil
}
