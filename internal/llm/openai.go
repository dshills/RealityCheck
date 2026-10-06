package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
)

// openaiProvider implements Provider using the OpenAI SDK.
type openaiProvider struct {
	client openai.Client
	model  string
	// noTemperature is set once the model rejects a temperature, so later
	// calls (such as the repair attempt) omit it instead of failing first.
	noTemperature atomic.Bool
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
	resp, err := p.Generate(ctx, Request{System: systemPrompt, User: userPrompt, MaxTokens: maxTokens, Temperature: temperature})
	return resp.Text, err
}

// openaiParams builds the request parameters. A schema is sent as a strict
// json_schema response format.
func openaiParams(model string, req Request) (openai.ChatCompletionNewParams, error) {
	params := openai.ChatCompletionNewParams{
		Model:               shared.ChatModel(model),
		MaxCompletionTokens: openai.Int(int64(req.MaxTokens)),
		Temperature:         openai.Float(req.Temperature),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(req.System),
			openai.UserMessage(req.User),
		},
	}
	if req.Schema != nil {
		schema, err := req.Schema.JSON()
		if err != nil {
			return params, fmt.Errorf("openai: output schema: %w", err)
		}
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   req.Schema.Name,
					Strict: openai.Bool(true),
					Schema: schema,
				},
			},
		}
	}
	return params, nil
}

// Generate sends one chat completion request. Reasoning models accept only
// the default temperature; when the API rejects it, the request is retried
// once without one.
func (p *openaiProvider) Generate(ctx context.Context, req Request) (Response, error) {
	params, err := openaiParams(p.model, req)
	if err != nil {
		return Response{}, err
	}
	if p.noTemperature.Load() {
		params.Temperature = param.Opt[float64]{}
	}
	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil && params.Temperature.Valid() && temperatureRejected(err) {
		p.noTemperature.Store(true)
		params.Temperature = param.Opt[float64]{}
		resp, err = p.client.Chat.Completions.New(ctx, params)
	}
	if err != nil {
		return Response{}, fmt.Errorf("openai: chat.completions.new: %w", err)
	}
	return openaiResponse(resp)
}

// temperatureRejected reports whether err is the model refusing any
// non-default temperature, as reasoning models do. An out-of-range value
// fails with a different code (decimal_above_max_value) and is returned.
func temperatureRejected(err error) bool {
	var apiErr *openai.Error
	return errors.As(err, &apiErr) &&
		apiErr.StatusCode == http.StatusBadRequest &&
		apiErr.Param == "temperature" &&
		(apiErr.Code == "unsupported_value" || apiErr.Code == "unsupported_parameter")
}

// openaiResponse extracts text and truncation. Empty content cut off at the
// token limit is returned as truncated rather than as an error, so Analyze
// can say to raise --max-tokens.
func openaiResponse(resp *openai.ChatCompletion) (Response, error) {
	if len(resp.Choices) == 0 {
		return Response{}, fmt.Errorf("openai: response contained no choices")
	}
	choice := resp.Choices[0]
	truncated := choice.FinishReason == "length"
	if choice.Message.Content == "" && !truncated {
		return Response{}, fmt.Errorf("openai: response contained no content")
	}
	return Response{Text: choice.Message.Content, Truncated: truncated}, nil
}
