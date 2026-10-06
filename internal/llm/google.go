package llm

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/genai"
)

// googleProvider implements Provider using the Google Gen AI SDK
// (google.golang.org/genai). It replaces the deprecated
// github.com/google/generative-ai-go SDK, whose Schema type cannot express
// property ordering; without it Gemini emits schema keys alphabetically,
// which would put coverage before drift and violations.
type googleProvider struct {
	client *genai.Client
	model  string
}

func newGoogleProvider(model string) (Provider, error) {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("llm: GOOGLE_API_KEY environment variable not set")
	}
	// NewClient only stores configuration; the context is not retained for
	// requests, which use the context passed to GenerateContent.
	//
	// The SDK logs a warning through the standard logger when both
	// GOOGLE_API_KEY and GEMINI_API_KEY are set, even though the key is
	// passed explicitly. It is left alone: silencing it would mean swapping
	// the process-wide logger, which can clobber an embedding application's
	// logging. Unset GEMINI_API_KEY to avoid the line.
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: google genai client: %w", err)
	}
	return &googleProvider{client: client, model: model}, nil
}

func (p *googleProvider) Complete(
	ctx context.Context,
	systemPrompt, userPrompt string,
	maxTokens int,
	temperature float64,
) (string, error) {
	resp, err := p.Generate(ctx, Request{System: systemPrompt, User: userPrompt, MaxTokens: maxTokens, Temperature: temperature})
	return resp.Text, err
}

// googleConfig builds the generation config. JSON output mode is always on
// so the model does not wrap the response in markdown fences; a schema is
// added as responseSchema with explicit property ordering.
func googleConfig(req Request) *genai.GenerateContentConfig {
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(req.System, genai.RoleUser),
		MaxOutputTokens:   int32(req.MaxTokens),
		Temperature:       genai.Ptr(float32(req.Temperature)),
		ResponseMIMEType:  "application/json",
	}
	if req.Schema != nil {
		cfg.ResponseSchema = req.Schema.genaiSchema()
	}
	return cfg
}

// Generate sends one generate-content request.
func (p *googleProvider) Generate(ctx context.Context, req Request) (Response, error) {
	resp, err := p.client.Models.GenerateContent(ctx, p.model, genai.Text(req.User), googleConfig(req))
	if err != nil {
		return Response{}, fmt.Errorf("google: generate content: %w", err)
	}
	return googleResponse(resp)
}

// googleResponse extracts text and truncation from a response. An empty
// response cut off at the token limit (e.g. the budget went to thinking) is
// returned as truncated rather than as an error, so Analyze can say to
// raise --max-tokens.
func googleResponse(resp *genai.GenerateContentResponse) (Response, error) {
	text := resp.Text()
	truncated := len(resp.Candidates) > 0 && resp.Candidates[0].FinishReason == genai.FinishReasonMaxTokens
	if text == "" && !truncated {
		return Response{}, fmt.Errorf("google: response contained no text content")
	}
	return Response{Text: text, Truncated: truncated}, nil
}
