package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/generative-ai-go/genai"
	googleoption "google.golang.org/api/option"
)

// googleProvider implements Provider using the Google Generative AI SDK.
// The API key is stored at construction time; a new genai.Client is created
// per Complete call so that the caller's context governs the connection and
// the client is always closed after use.
type googleProvider struct {
	apiKey string
	model  string
}

func newGoogleProvider(model string) (Provider, error) {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("llm: GOOGLE_API_KEY environment variable not set")
	}
	return &googleProvider{apiKey: apiKey, model: model}, nil
}

func (p *googleProvider) Complete(
	ctx context.Context,
	systemPrompt, userPrompt string,
	maxTokens int,
	temperature float64,
) (string, error) {
	client, err := genai.NewClient(ctx, googleoption.WithAPIKey(p.apiKey))
	if err != nil {
		return "", fmt.Errorf("google: genai client: %w", err)
	}
	defer client.Close()

	m := client.GenerativeModel(p.model)
	m.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(systemPrompt)},
	}
	maxOut := int32(maxTokens)
	m.MaxOutputTokens = &maxOut
	temp32 := float32(temperature)
	m.Temperature = &temp32
	// Force JSON output mode to prevent the model from wrapping the response
	// in markdown code fences.
	m.ResponseMIMEType = "application/json"

	resp, err := m.GenerateContent(ctx, genai.Text(userPrompt))
	if err != nil {
		return "", fmt.Errorf("google: generate content: %w", err)
	}

	var parts []string
	for _, cand := range resp.Candidates {
		if cand.Content == nil {
			continue
		}
		for _, part := range cand.Content.Parts {
			if t, ok := part.(genai.Text); ok {
				parts = append(parts, string(t))
			}
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("google: response contained no text content")
	}
	return strings.Join(parts, ""), nil
}
