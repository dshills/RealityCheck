package llm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go"
	"google.golang.org/genai"
)

// assertOrder fails unless each key appears in data after the previous one.
func assertOrder(t *testing.T, data []byte, keys ...string) {
	t.Helper()
	last := -1
	for _, k := range keys {
		i := bytes.Index(data, []byte(`"`+k+`"`))
		if i < 0 {
			t.Fatalf("key %q not found in %s", k, data)
		}
		if i < last {
			t.Errorf("key %q is out of order in %s", k, data)
		}
		last = i
	}
}

func TestReportSchema_JSON(t *testing.T) {
	raw, err := reportSchema.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	assertOrder(t, raw, "drift", "violations", "coverage")
	if v["additionalProperties"] != false {
		t.Error("objects must disallow additional properties (OpenAI strict mode)")
	}
	req, _ := v["required"].([]any)
	if len(req) != 3 {
		t.Errorf("required = %v, want all three top-level keys", req)
	}
	for _, banned := range []string{"spec_reference", "plan_reference", "quote", "meta"} {
		if strings.Contains(string(raw), `"`+banned+`"`) {
			t.Errorf("schema asks for derived field %q", banned)
		}
	}
	for _, sch := range []*OutputSchema{reportSchema, completionOutputSchema} {
		raw, err := sch.JSON()
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		assertStrict(t, sch.Name, decoded)
	}
}

// assertStrict checks OpenAI strict-mode rules on every object in a decoded
// schema: required lists exactly the properties, and no extra properties.
func assertStrict(t *testing.T, path string, node map[string]any) {
	t.Helper()
	if node["type"] == "object" {
		props, _ := node["properties"].(map[string]any)
		req, _ := node["required"].([]any)
		if len(req) != len(props) {
			t.Errorf("%s: required %v does not list all %d properties", path, req, len(props))
		}
		seen := map[string]bool{}
		for _, r := range req {
			name := r.(string)
			if seen[name] {
				t.Errorf("%s: required lists %q twice", path, name)
			}
			seen[name] = true
			if _, ok := props[name]; !ok {
				t.Errorf("%s: required %q is not a property", path, name)
			}
		}
		if node["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties must be false", path)
		}
		for name, child := range props {
			assertStrict(t, path+"."+name, child.(map[string]any))
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		assertStrict(t, path+"[]", items)
	}
}

func TestAnthropicParams_PreserveSchemaOrder(t *testing.T) {
	params, err := anthropicParams("m", Request{System: "s", User: "u", MaxTokens: 10, Schema: reportSchema})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"output_config"`)) || !bytes.Contains(body, []byte(`"json_schema"`)) {
		t.Fatalf("request lacks output_config json_schema: %s", body)
	}
	assertOrder(t, body, "drift", "violations", "coverage")

	plain, _ := anthropicParams("m", Request{System: "s", User: "u", MaxTokens: 10})
	body, _ = json.Marshal(plain)
	if bytes.Contains(body, []byte(`"output_config"`)) {
		t.Error("no schema should mean no output_config")
	}
}

func TestOpenAIParams_PreserveSchemaOrder(t *testing.T) {
	params, err := openaiParams("m", Request{System: "s", User: "u", MaxTokens: 10, Schema: reportSchema})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"response_format"`, `"strict":true`, `"name":"realitycheck_report"`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("request missing %s: %s", want, body)
		}
	}
	assertOrder(t, body, "drift", "violations", "coverage")
}

func TestGoogleConfig_PropertyOrdering(t *testing.T) {
	cfg := googleConfig(Request{System: "s", MaxTokens: 10, Temperature: 0.2, Schema: reportSchema})
	if cfg.ResponseMIMEType != "application/json" || cfg.MaxOutputTokens != 10 {
		t.Errorf("config = %+v", cfg)
	}
	root := cfg.ResponseSchema
	if root == nil {
		t.Fatal("missing response schema")
	}
	if got := strings.Join(root.PropertyOrdering, ","); got != "drift,violations,coverage" {
		t.Errorf("root ordering = %s", got)
	}
	// Every object at every depth must carry explicit ordering.
	var walk func(path string, s *genai.Schema)
	walk = func(path string, s *genai.Schema) {
		if s.Type == genai.TypeObject && len(s.PropertyOrdering) != len(s.Properties) {
			t.Errorf("%s: ordering %v does not cover properties", path, s.PropertyOrdering)
		}
		for name, child := range s.Properties {
			walk(path+"."+name, child)
		}
		if s.Items != nil {
			walk(path+"[]", s.Items)
		}
	}
	walk("$", root)
	if cfg := googleConfig(Request{}); cfg.ResponseSchema != nil {
		t.Error("no schema should mean no responseSchema")
	}
}

func TestProviderResponses_EmptyButTruncated(t *testing.T) {
	g, err := googleResponse(&genai.GenerateContentResponse{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonMaxTokens}}})
	if err != nil || !g.Truncated || g.Text != "" {
		t.Errorf("google: %+v, %v", g, err)
	}
	if _, err := googleResponse(&genai.GenerateContentResponse{Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonStop}}}); err == nil {
		t.Error("google: empty, not truncated should error")
	}

	var oc openai.ChatCompletion
	if err := json.Unmarshal([]byte(`{"choices":[{"finish_reason":"length","message":{"content":""}}]}`), &oc); err != nil {
		t.Fatal(err)
	}
	if o, err := openaiResponse(&oc); err != nil || !o.Truncated {
		t.Errorf("openai: %+v, %v", o, err)
	}
	if err := json.Unmarshal([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":""}}]}`), &oc); err != nil {
		t.Fatal(err)
	}
	if _, err := openaiResponse(&oc); err == nil {
		t.Error("openai: empty, not truncated should error")
	}

	var am anthropic.Message
	if err := json.Unmarshal([]byte(`{"content":[],"stop_reason":"max_tokens"}`), &am); err != nil {
		t.Fatal(err)
	}
	if a, err := anthropicResponse(&am); err != nil || !a.Truncated {
		t.Errorf("anthropic: %+v, %v", a, err)
	}
	if err := json.Unmarshal([]byte(`{"content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn"}`), &am); err != nil {
		t.Fatal(err)
	}
	if a, err := anthropicResponse(&am); err != nil || a.Truncated || a.Text != "{}" {
		t.Errorf("anthropic text: %+v, %v", a, err)
	}
}
