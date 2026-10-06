package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const openaiOK = `{"id":"x","object":"chat.completion","created":0,"model":"m",
"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}]}`

// openaiTestServer answers chat completions, rejecting any request that sets
// temperature when rejectTemperature is true, and any temperature above 2 as
// out of range. The returned func snapshots the request bodies received.
func openaiTestServer(t *testing.T, rejectTemperature bool) (*openaiProvider, func() []map[string]any) {
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
		w.Header().Set("Content-Type", "application/json")
		if temp, ok := body["temperature"].(float64); ok && temp > 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid 'temperature': decimal above maximum value.",`+
				`"type":"invalid_request_error","param":"temperature","code":"decimal_above_max_value"}}`)
			return
		}
		if _, ok := body["temperature"]; ok && rejectTemperature {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unsupported value: 'temperature' does not support 0.2 with this model.",`+
				`"type":"invalid_request_error","param":"temperature","code":"unsupported_value"}}`)
			return
		}
		_, _ = io.WriteString(w, openaiOK)
	}))
	t.Cleanup(srv.Close)
	client := openai.NewClient(option.WithAPIKey("k"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	snapshot := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
	return &openaiProvider{client: client, model: "m"}, snapshot
}

func TestOpenAIGenerate_RetriesWithoutRejectedTemperature(t *testing.T) {
	p, requests := openaiTestServer(t, true)
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

func TestOpenAIGenerate_KeepsAcceptedTemperature(t *testing.T) {
	p, requests := openaiTestServer(t, false)
	if _, err := p.Generate(context.Background(), Request{System: "s", User: "u", MaxTokens: 10, Temperature: 0.2}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(requests()) != 1 || requests()[0]["temperature"] != 0.2 {
		t.Errorf("requests = %v, want one request with temperature 0.2", requests())
	}
}

func TestOpenAIGenerate_OutOfRangeTemperatureFails(t *testing.T) {
	p, requests := openaiTestServer(t, false)
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

func TestTemperatureRejected(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&openai.Error{StatusCode: http.StatusBadRequest, Param: "temperature", Code: "unsupported_value"}, true},
		{&openai.Error{StatusCode: http.StatusBadRequest, Param: "temperature", Code: "unsupported_parameter"}, true},
		// An out-of-range value is the caller's mistake, not a model restriction.
		{&openai.Error{StatusCode: http.StatusBadRequest, Param: "temperature", Code: "decimal_above_max_value"}, false},
		{&openai.Error{StatusCode: http.StatusBadRequest, Param: "max_tokens", Code: "unsupported_parameter"}, false},
		{&openai.Error{StatusCode: http.StatusInternalServerError, Param: "temperature", Code: "unsupported_value"}, false},
		{io.EOF, false},
	} {
		if got := temperatureRejected(tc.err); got != tc.want {
			t.Errorf("temperatureRejected(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
