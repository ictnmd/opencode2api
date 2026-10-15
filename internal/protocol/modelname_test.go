package protocol

import (
	"io"
	"strings"
	"testing"
)

func TestRewriteResponseModelChat(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-5.6","choices":[],"usage":{"prompt_tokens":1}}`)
	got, changed := RewriteResponseModel(body, "gpt-5.5")
	if !changed {
		t.Fatal("expected the model field to change")
	}
	if !strings.Contains(string(got), `"model":"gpt-5.5"`) {
		t.Fatalf("model not rewritten: %s", got)
	}
	if strings.Contains(string(got), "gpt-5.6") {
		t.Fatalf("upstream model leaked: %s", got)
	}
}

func TestRewriteResponseModelAnthropicAndResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "anthropic message",
			body: `{"id":"msg_1","type":"message","model":"real-model","content":[]}`,
			want: `"model":"alias"`,
		},
		{
			name: "responses envelope",
			body: `{"type":"response.completed","response":{"id":"resp_1","model":"real-model","output":[]}}`,
			want: `"model":"alias"`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, changed := RewriteResponseModel([]byte(test.body), "alias")
			if !changed {
				t.Fatal("expected a change")
			}
			if !strings.Contains(string(got), test.want) {
				t.Fatalf("want %s in %s", test.want, got)
			}
		})
	}
}

// museSparkAlias and museSparkModel are the exact pair from the operator
// configuration, shared by the protocol tests below. The requested name is a
// prefix of the real ID, so every assertion matches the quoted JSON value (with
// its closing quote) rather than a bare substring.
const (
	museSparkAlias = "muse-spark-1.3"
	museSparkModel = "muse-spark-1.3-contributor-free"
)

// The upstream document names the real model ID; the client must still see the
// name it asked for, in every shape the three protocols use for it.
func TestRewriteResponseModelMuseSparkAlias(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "chat top level",
			body: `{"id":"chatcmpl-1","object":"chat.completion","model":"muse-spark-1.3-contributor-free","choices":[]}`,
			want: `"model":"muse-spark-1.3"`,
		},
		{
			name: "anthropic message",
			body: `{"id":"msg_1","type":"message","role":"assistant","model":"muse-spark-1.3-contributor-free","content":[]}`,
			want: `"model":"muse-spark-1.3"`,
		},
		{
			name: "anthropic stream envelope",
			body: `{"type":"message_start","message":{"id":"msg_1","model":"muse-spark-1.3-contributor-free","content":[]}}`,
			want: `"model":"muse-spark-1.3"`,
		},
		{
			name: "responses envelope",
			body: `{"type":"response.completed","response":{"id":"resp_1","model":"muse-spark-1.3-contributor-free","output":[]}}`,
			want: `"model":"muse-spark-1.3"`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, changed := RewriteResponseModel([]byte(test.body), museSparkAlias)
			if !changed {
				t.Fatal("expected the model field to change")
			}
			if !strings.Contains(string(got), test.want) {
				t.Fatalf("want %s in %s", test.want, got)
			}
			if strings.Contains(string(got), museSparkModel) {
				t.Fatalf("real model ID leaked to the client: %s", got)
			}
		})
	}
}

// The same pair must survive the streaming rewrite, for each protocol's frame
// shape, without disturbing frames that carry no model field.
func TestModelRewriteReaderMuseSparkAlias(t *testing.T) {
	cases := []struct {
		name   string
		frames string
		want   int
	}{
		{
			name: "chat",
			frames: "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"muse-spark-1.3-contributor-free\",\"choices\":[]}\n\n" +
				"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"muse-spark-1.3-contributor-free\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
				"data: [DONE]\n\n",
			want: 2,
		},
		{
			name: "anthropic",
			frames: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"muse-spark-1.3-contributor-free\",\"content\":[]}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			want: 1,
		},
		{
			name: "responses",
			frames: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"muse-spark-1.3-contributor-free\",\"output\":[]}}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"muse-spark-1.3-contributor-free\",\"output\":[]}}\n\n",
			want: 2,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := io.ReadAll(NewModelRewriteReader(strings.NewReader(test.frames), museSparkAlias))
			if err != nil {
				t.Fatal(err)
			}
			text := string(got)
			if strings.Contains(text, museSparkModel) {
				t.Fatalf("real model ID leaked into the stream: %s", text)
			}
			if count := strings.Count(text, `"model":"muse-spark-1.3"`); count != test.want {
				t.Fatalf("expected %d rewritten frames, got %d: %s", test.want, count, text)
			}
			if !strings.HasSuffix(text, "\n\n") {
				t.Fatalf("frame delimiter lost: %q", text)
			}
		})
	}
}

func TestRewriteResponseModelLeavesPayloadWithoutModel(t *testing.T) {
	body := []byte(`{"type":"message_stop"}`)
	got, changed := RewriteResponseModel(body, "alias")
	if changed {
		t.Fatal("a payload without a model field must not change")
	}
	if string(got) != string(body) {
		t.Fatalf("body altered: %s", got)
	}
}

func TestRewriteResponseModelKeepsLargeIntegers(t *testing.T) {
	// 2^53+1 is not representable as float64; decoding with UseNumber keeps it.
	body := []byte(`{"model":"real","created":9007199254740993}`)
	got, _ := RewriteResponseModel(body, "alias")
	if !strings.Contains(string(got), "9007199254740993") {
		t.Fatalf("integer rounded: %s", got)
	}
}

func TestRewriteResponseModelInvalidJSON(t *testing.T) {
	body := []byte(`{"model":`)
	got, changed := RewriteResponseModel(body, "alias")
	if changed {
		t.Fatal("invalid JSON must be left alone")
	}
	if string(got) != string(body) {
		t.Fatalf("body altered: %s", got)
	}
}

func TestModelRewriteReaderStreamsFrames(t *testing.T) {
	upstream := "data: {\"id\":\"1\",\"model\":\"real\",\"choices\":[]}\n\n" +
		"data: {\"id\":\"1\",\"model\":\"real\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(NewModelRewriteReader(strings.NewReader(upstream), "alias"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, `"model":"real"`) {
		t.Fatalf("upstream model leaked: %s", text)
	}
	if strings.Count(text, `"model":"alias"`) != 2 {
		t.Fatalf("expected both model frames rewritten: %s", text)
	}
	if !strings.HasSuffix(text, "data: [DONE]\n\n") {
		t.Fatalf("[DONE] terminator lost: %q", text)
	}
}

func TestModelRewriteReaderPreservesChunkBoundaries(t *testing.T) {
	// A frame split across reads must not be rewritten until it is complete.
	frame := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"real\"}}\n\n"
	reader := NewModelRewriteReader(&chunkedReader{chunks: []string{frame[:20], frame[20:40], frame[40:]}}, "alias")
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"model":"alias"`) {
		t.Fatalf("frame not rewritten: %s", got)
	}
	if !strings.HasPrefix(string(got), "event: message_start\n") {
		t.Fatalf("event name lost: %s", got)
	}
}

func TestModelRewriteReaderLeavesTrailingFragment(t *testing.T) {
	// An unterminated trailing fragment is not a frame and must pass through.
	upstream := "data: {\"model\":\"real\"}\n\ndata: {\"model\":\"rea"
	got, err := io.ReadAll(NewModelRewriteReader(strings.NewReader(upstream), "alias"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(got), "data: {\"model\":\"rea") {
		t.Fatalf("trailing fragment altered: %q", got)
	}
}

func TestModelRewriteReaderEmptyNameIsTransparent(t *testing.T) {
	source := strings.NewReader("data: {\"model\":\"real\"}\n\n")
	if got := NewModelRewriteReader(source, ""); got != io.Reader(source) {
		t.Fatal("an empty name must return the source reader unchanged")
	}
}

type chunkedReader struct {
	chunks []string
	index  int
}

func (reader *chunkedReader) Read(target []byte) (int, error) {
	if reader.index >= len(reader.chunks) {
		return 0, io.EOF
	}
	chunk := reader.chunks[reader.index]
	reader.index++
	return copy(target, chunk), nil
}
