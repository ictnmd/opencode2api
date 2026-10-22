package protocol

import (
	"net/http"

	"opencode2api/internal/httpx"
)

// WriteError writes a JSON error response with the given status in the
// envelope the protocol's clients expect: {"type":"error","error":{...}} for
// Anthropic, and the OpenAI {"error":{message,type,param,code}} shape for
// every other protocol, System One included. kind becomes the error type, and
// a non-empty requestID is echoed in the x-request-id header.
func WriteError(w http.ResponseWriter, protocol Protocol, status int, message, kind, requestID string) {
	WriteErrorCode(w, protocol, status, message, kind, "", requestID)
}

// WriteErrorCode is WriteError with the error code an upstream reported,
// kept in the envelope's code slot. An empty code leaves the slot empty, so a
// failure the gateway detected itself carries no code, as before. The
// Anthropic envelope has no code slot, so the code is dropped there and the
// upstream's own message carries the reason.
func WriteErrorCode(w http.ResponseWriter, protocol Protocol, status int, message, kind, code, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("x-request-id", requestID)
	}
	if protocol == Anthropic {
		httpx.WriteJSONStatus(w, status, map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
		return
	}
	httpx.WriteJSONStatus(w, status, map[string]any{"error": map[string]any{"message": message, "type": kind, "param": nil, "code": errorCode(code)}})
}

// errorCode returns the value of an envelope's code slot: the code the
// upstream reported, or null when it reported none.
func errorCode(code string) any {
	if code == "" {
		return nil
	}
	return code
}

// WriteSimpleError writes the compact error body the simple-body mode uses:
// {"error":{"code":...,"message":...}} for every protocol, carrying the error
// code and the message and nothing else. The code is the same discriminator
// WriteError puts in the protocol's own envelope, so a client reads it from
// error.code instead of error.type.
func WriteSimpleError(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("x-request-id", requestID)
	}
	httpx.WriteJSONStatus(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
