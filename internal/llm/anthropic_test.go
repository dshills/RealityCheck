package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const anthropicOK = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`

const (
	anthropicDeprecatedTemp = `{"type":"error","error":{"type":"invalid_request_error",` +
		"\"message\":\"`temperature` is deprecated for this model.\"}}"
	anthropicOutOfRangeTemp = `{"type":"error","error":{"type":"invalid_request_error",` +
		`"message":"temperature: Input should be less than or equal to 1"}}`
)

// anthropicTestServer streams a message, rejecting any request that sets
// temperature when rejectTemperature is true, and any temperature above 1 as
// out of range. The returned func snapshots the request bodies received.
func anthropicTestServer(t *testing.T, rejectTemperature bool) (*anthropicProvider, func() []map[string]any) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if temp, ok := body["temperature"].(float64); ok && temp > 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, anthropicOutOfRangeTemp)
			return
		}
		if _, ok := body["temperature"]; ok && rejectTemperature {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, anthropicDeprecatedTemp)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicOK)
	}))
	t.Cleanup(srv.Close)
	client := anthropic.NewClient(option.WithAPIKey("k"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	snapshot := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
	return &anthropicProvider{client: client, model: "m"}, snapshot
}

func TestAnthropicGenerate_RetriesWithoutRejectedTemperature(t *testing.T) {
	p, requests := anthropicTestServer(t, true)
	req := Request{System: "s", User: "u", MaxTokens: 10, Temperature: 0.2}
	resp, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "{}" {
		t.Errorf("text = %q", resp.Text)
	}
	if len(requests()) != 2 {
		t.Fatalf("requests = %d, want 2 (rejected, then retried)", len(requests()))
	}
	if _, ok := requests()[1]["temperature"]; ok {
		t.Error("retry still sent temperature")
	}

	// Later calls on the same provider skip the doomed first attempt.
	if _, err := p.Generate(context.Background(), req); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if len(requests()) != 3 {
		t.Errorf("requests = %d, want 3", len(requests()))
	}
}

func TestAnthropicGenerate_KeepsAcceptedTemperature(t *testing.T) {
	p, requests := anthropicTestServer(t, false)
	if _, err := p.Generate(context.Background(), Request{System: "s", User: "u", MaxTokens: 10, Temperature: 0.2}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(requests()) != 1 || requests()[0]["temperature"] != 0.2 {
		t.Errorf("requests = %v, want one request with temperature 0.2", requests())
	}
}

func TestAnthropicGenerate_OutOfRangeTemperatureFails(t *testing.T) {
	p, requests := anthropicTestServer(t, false)
	if _, err := p.Generate(context.Background(), Request{System: "s", User: "u", MaxTokens: 10, Temperature: 3}); err == nil {
		t.Fatal("out-of-range temperature should fail, not fall back to the default")
	}
	if len(requests()) != 1 {
		t.Errorf("requests = %d, want 1 (no retry)", len(requests()))
	}
	// A valid temperature on a later call is still sent.
	if _, err := p.Generate(context.Background(), Request{System: "s", User: "u", MaxTokens: 10, Temperature: 0.2}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if requests()[1]["temperature"] != 0.2 {
		t.Errorf("later request temperature = %v, want 0.2", requests()[1]["temperature"])
	}
}

// anthropicAPIError builds the SDK's error for a response, as the client does.
func anthropicAPIError(t *testing.T, status int, body string) error {
	t.Helper()
	apiErr := &anthropic.Error{StatusCode: status}
	if err := apiErr.UnmarshalJSON([]byte(body)); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return fmt.Errorf("anthropic: messages stream: %w", apiErr)
}

func TestAnthropicTemperatureRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"deprecated", anthropicAPIError(t, http.StatusBadRequest, anthropicDeprecatedTemp), true},
		{"not supported", anthropicAPIError(t, http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error",`+
			`"message":"temperature is not supported for this model"}}`), true},
		// An out-of-range value is the caller's mistake, not a model restriction.
		{"out of range", anthropicAPIError(t, http.StatusBadRequest, anthropicOutOfRangeTemp), false},
		{"other parameter", anthropicAPIError(t, http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error",`+
			`"message":"top_k is deprecated for this model."}}`), false},
		{"server error", anthropicAPIError(t, http.StatusInternalServerError, `{"type":"error","error":{"type":"api_error",`+
			"\"message\":\"`temperature` is deprecated for this model.\"}}"), false},
		{"unparseable body", anthropicAPIError(t, http.StatusBadRequest, `"temperature is deprecated"`), false},
		{"not an API error", errors.New("temperature is deprecated"), false},
	} {
		if got := anthropicTemperatureRejected(tc.err); got != tc.want {
			t.Errorf("%s: anthropicTemperatureRejected = %v, want %v", tc.name, got, tc.want)
		}
	}
}
