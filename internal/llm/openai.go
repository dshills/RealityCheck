package llm

import (
	"context"
	"fmt"
	"os"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// openaiProvider implements Provider using the OpenAI SDK.
type openaiProvider struct {
	client openai.Client
	model  string
}

func newOpenAIProvider(model string) (Provider, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("llm: OPENAI_API_KEY environment variable not set")
	}
	client := openai.NewClient(option.WithAPIKey(apiKey))
	return &openaiProvider{client: client, model: model}, nil
}

func (p *openaiProvider) Complete(
	ctx context.Context,
	systemPrompt, userPrompt string,
	maxTokens int,
	temperature float64,
) (string, error) {
	resp, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:     shared.ChatModel(p.model),
		MaxTokens: openai.Int(int64(maxTokens)),
		Temperature: openai.Float(temperature),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
	})
	if err != nil {
		return "", fmt.Errorf("openai: chat.completions.new: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("openai: response contained no choices")
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		return "", fmt.Errorf("openai: response contained no content")
	}
	return content, nil
}
