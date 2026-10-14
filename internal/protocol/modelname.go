package protocol

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Model names are rewritten on the way out when an operator publishes a model
// under a different client-facing name. The client asked for one name, the
// request was routed to the real model behind it, and the reply must report the
// name the client used — otherwise a client that pins a model name in its own
// configuration sees its request answered by something it never asked for.
//
// Only the model field is touched. Everything else in the upstream document or
// stream is forwarded as-is, and a payload that carries no model field is
// returned byte-for-byte, so a deployment without aliases stays transparent.

// RewriteResponseModel replaces the model name in a complete (non-streaming)
// response document. It reports whether the document changed; an unparseable
// body, or one without a model field, is returned unchanged.
func RewriteResponseModel(body []byte, name string) ([]byte, bool) {
	if name == "" || len(body) == 0 {
		return body, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	// UseNumber keeps integers exact: re-marshalling float64 would silently
	// round any identifier or counter above 2^53.
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return body, false
	}
	if !rewriteModelValue(value, name) {
		return body, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return body, false
	}
	return encoded, true
}

// rewriteModelValue sets the model name on whichever field carries it for the
// supported protocols: the top level (Chat), message.model (Anthropic) and
// response.model (Responses). None of those keys holds anything but the model
// name, so a payload that lacks them is left alone.
func rewriteModelValue(value map[string]any, name string) bool {
	changed := false
	for _, path := range [][]string{{"model"}, {"message", "model"}, {"response", "model"}} {
		container := value
		for _, key := range path[:len(path)-1] {
			nested, ok := container[key].(map[string]any)
			if !ok {
				container = nil
				break
			}
			container = nested
		}
		if container == nil {
			continue
		}
		key := path[len(path)-1]
		current, ok := container[key].(string)
		if !ok || current == name {
			continue
		}
		container[key] = name
		changed = true
	}
	return changed
}

// NewModelRewriteReader forwards an upstream SSE stream while replacing the
// model name in every frame that carries one. Frames without a model field are
// emitted unchanged, so byte fidelity is preserved everywhere it matters and a
// caller that passes an empty name gets the source back untouched.
func NewModelRewriteReader(source io.Reader, name string) io.Reader {
	if name == "" {
		return source
	}
	return &modelRewriteReader{source: source, name: name}
}

type modelRewriteReader struct {
	source  io.Reader
	name    string
	input   []byte
	output  []byte
	scratch []byte
	done    bool
	err     error
}

func (reader *modelRewriteReader) Read(target []byte) (int, error) {
	for len(reader.output) == 0 && !reader.done && reader.err == nil {
		reader.pull()
	}
	if len(reader.output) > 0 {
		count := copy(target, reader.output)
		reader.output = reader.output[count:]
		return count, nil
	}
	if reader.err != nil {
		return 0, reader.err
	}
	return 0, io.EOF
}

// pull reads until it has one more complete frame to emit, or until the source
// is exhausted. A frame is emitted only once its blank-line delimiter has been
// seen, which keeps a partial frame from being rewritten as if it were whole.
func (reader *modelRewriteReader) pull() {
	if reader.scratch == nil {
		reader.scratch = make([]byte, 32<<10)
	}
	idle := 0
	for {
		if index, width := nextSSEBoundary(reader.input); index >= 0 {
			frame := reader.input[:index+width]
			reader.input = reader.input[index+width:]
			reader.output = rewriteSSEFrameModel(frame, reader.name)
			return
		}
		count, err := reader.source.Read(reader.scratch)
		if count > 0 {
			reader.input = append(reader.input, reader.scratch[:count]...)
			idle = 0
			continue
		}
		if err == nil {
			// A reader may legally return (0, nil). Bound the retries so a
			// misbehaving source cannot spin this loop forever.
			if idle++; idle > 100 {
				reader.err = io.ErrNoProgress
				return
			}
			continue
		}
		if err != io.EOF {
			reader.err = err
			return
		}
		reader.done = true
		if len(reader.input) > 0 {
			// A trailing fragment without a delimiter is not a frame. Forward it
			// untouched rather than re-serializing something that may be
			// truncated.
			reader.output = reader.input
			reader.input = nil
		}
		return
	}
}

// rewriteSSEFrameModel rewrites the model name inside one complete SSE frame,
// returning the frame unchanged when it carries no model field.
func rewriteSSEFrameModel(frame []byte, name string) []byte {
	if !bytes.Contains(frame, []byte(`"model"`)) {
		return frame
	}
	body, ok := trimSSEDelimiter(frame)
	if !ok {
		return frame
	}
	lines := strings.Split(body, "\n")
	dataIndexes := make([]int, 0, 1)
	joined := make([]string, 0, 1)
	for index, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataIndexes = append(dataIndexes, index)
		joined = append(joined, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	if len(dataIndexes) == 0 {
		return frame
	}
	payload := strings.Join(joined, "\n")
	if payload == "" || payload == "[DONE]" {
		return frame
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return frame
	}
	if !rewriteModelValue(value, name) {
		return frame
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return frame
	}
	first := dataIndexes[0]
	drop := make(map[int]bool, len(dataIndexes))
	for _, index := range dataIndexes {
		drop[index] = true
	}
	var builder strings.Builder
	builder.Grow(len(frame))
	for index, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if drop[index] {
			if index == first {
				builder.WriteString("data: ")
				builder.Write(encoded)
				builder.WriteString("\n")
			}
			continue
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	return []byte(builder.String())
}

// trimSSEDelimiter removes the blank line that terminates an SSE frame. A
// fragment without one is not a frame and must not be re-serialized.
func trimSSEDelimiter(frame []byte) (string, bool) {
	text := string(frame)
	switch {
	case strings.HasSuffix(text, "\r\n\r\n"):
		return text[:len(text)-4], true
	case strings.HasSuffix(text, "\n\n"):
		return text[:len(text)-2], true
	default:
		return "", false
	}
}
