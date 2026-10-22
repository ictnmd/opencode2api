package protocol

import "testing"

func TestPrepareRequestNormalizesCompetingOutputTokenNames(t *testing.T) {
	tests := []struct {
		name     string
		protocol Protocol
		input    map[string]any
		wantKey  string
		want     any
	}{
		{
			name:     "chat prefers max_tokens",
			protocol: Chat,
			input:    map[string]any{"model": "glm-5.3-flash-free", "max_tokens": float64(128), "max_output_tokens": float64(256), "max_completion_tokens": float64(512)},
			wantKey:  "max_tokens",
			want:     float64(128),
		},
		{
			name:     "chat accepts completion alias",
			protocol: Chat,
			input:    map[string]any{"model": "glm-5.3-flash-free", "max_completion_tokens": float64(512)},
			wantKey:  "max_tokens",
			want:     float64(512),
		},
		{
			name:     "responses prefers max_output_tokens",
			protocol: Responses,
			input:    map[string]any{"model": "glm-5.3-flash-free", "max_tokens": float64(128), "max_output_tokens": float64(256), "max_completion_tokens": float64(512)},
			wantKey:  "max_output_tokens",
			want:     float64(256),
		},
		{
			name:     "anthropic prefers max_tokens",
			protocol: Anthropic,
			input:    map[string]any{"model": "glm-5.3-flash-free", "max_tokens": float64(128), "max_output_tokens": float64(256), "max_completion_tokens": float64(512)},
			wantKey:  "max_tokens",
			want:     float64(128),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := PrepareRequest(test.protocol, test.protocol, test.input, "")
			if err != nil {
				t.Fatalf("PrepareRequest: %v", err)
			}
			if output[test.wantKey] != test.want {
				t.Fatalf("%s = %v, want %v; body=%v", test.wantKey, output[test.wantKey], test.want, output)
			}
			for _, key := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
				if key != test.wantKey {
					if _, ok := output[key]; ok {
						t.Fatalf("foreign token field %q leaked into output: %v", key, output)
					}
				}
			}
		})
	}
}
