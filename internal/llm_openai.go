package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// modelReasoningEffortPrefixes maps model name prefixes to the lowest supported
// reasoning effort level. Entries are checked longest-prefix-first so that e.g.
// "gpt-5.4" matches before "gpt-5". Models not matching any prefix (like gpt-4o
// or gpt-4.1, which are non-reasoning models) will not have reasoning_effort set.
var modelReasoningEffortPrefixes = []struct {
	prefix string
	effort string
}{
	// GPT-5.4 family — supports none, low, medium, high (default: none)
	{"gpt-5.4-nano", "none"},
	{"gpt-5.4-mini", "none"},
	{"gpt-5.4", "none"},
	// GPT-5.3 family
	{"gpt-5.3", "none"},
	// GPT-5.2 family — supports none, low, medium, high, xhigh (default: none)
	{"gpt-5.2", "none"},
	// GPT-5.1 family — supports none, low, medium, high (default: none)
	{"gpt-5.1-mini", "none"},
	{"gpt-5.1", "none"},
	// GPT-5 family — supports minimal, low, medium, high (default: medium)
	{"gpt-5-nano", "minimal"},
	{"gpt-5-mini", "minimal"},
	{"gpt-5", "minimal"},
	// o-series — supports low, medium, high (default: medium)
	{"o4-mini", "low"},
	{"o3-pro", "low"},
	{"o3-mini", "low"},
	{"o3", "low"},
	{"o1-pro", "low"},
	{"o1", "low"},
}

// defaultReasoningEffort returns the lowest reasoning effort for the given model,
// or an empty string if the model doesn't support reasoning effort.
func defaultReasoningEffort(model string) string {
	for _, entry := range modelReasoningEffortPrefixes {
		if strings.HasPrefix(model, entry.prefix) {
			return entry.effort
		}
	}
	return ""
}

// OpenAILLMClient handles communication with the OpenAI API
type OpenAILLMClient struct {
	client          openai.Client
	model           string
	reasoningEffort string // empty string means don't set reasoning effort
	history         []openai.ChatCompletionMessageParamUnion
}

// NewOpenAILLMClient creates a new OpenAI LLM client.
// It reads the "reasoning_effort" option from opts; if absent, defaults to the
// lowest supported effort for the model.
func NewOpenAILLMClient(apiKey string, model string, opts map[string]string, systemPrompt string) *OpenAILLMClient {
	client := openai.NewClient(option.WithAPIKey(apiKey))

	reasoningEffort := opts["reasoning_effort"]
	if reasoningEffort == "" {
		reasoningEffort = defaultReasoningEffort(model)
	}

	history := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
	}

	return &OpenAILLMClient{
		client:          client,
		model:           model,
		reasoningEffort: reasoningEffort,
		history:         history,
	}
}

// Query sends a query to the OpenAI API and returns the structured response
func (c *OpenAILLMClient) Query(ctx context.Context, queryString string) (*LLMResponse, error) {
	logger := LoggerFromContext(ctx)
	logger.Debug("Calling OpenAI API", zap.String("model", c.model))

	startTime := time.Now()

	c.history = append(c.history, openai.UserMessage(queryString))

	schema, err := jsonschema.For[LLMResponse](nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate schema: %w", err)
	}

	params := openai.ChatCompletionNewParams{
		Messages: c.history,
		Model:    shared.ChatModel(c.model),
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "postgres_response",
					Schema: schema,
				},
			},
		},
	}

	if c.reasoningEffort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(c.reasoningEffort)
	}

	completion, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("OpenAI API call failed: %w", err)
	}

	duration := time.Since(startTime)

	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("no choices in OpenAI response")
	}

	content := completion.Choices[0].Message.Content
	logger.Debug("OpenAI API call complete",
		zap.Int64("duration_ms", duration.Milliseconds()),
		zap.Int64("prompt_tokens", completion.Usage.PromptTokens),
		zap.Int64("completion_tokens", completion.Usage.CompletionTokens),
	)

	var response LLMResponse
	if err := json.Unmarshal([]byte(content), &response); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	c.history = append(c.history, openai.AssistantMessage(content))

	return &response, nil
}
