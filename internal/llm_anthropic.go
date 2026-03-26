package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/google/jsonschema-go/jsonschema"
)

// AnthropicLLMClient handles communication with the Anthropic API
type AnthropicLLMClient struct {
	client   anthropic.Client
	model    string
	effort   anthropic.OutputConfigEffort // empty means don't set
	thinking anthropic.ThinkingConfigParamUnion
	history  []anthropic.MessageParam
}

// NewAnthropicLLMClient creates a new Anthropic LLM client.
// Supported options:
//   - "effort": output effort level (low, medium, high, max). Defaults to "low".
//   - "thinking": extended thinking budget in tokens (e.g. "10000"). Minimum 1024.
func NewAnthropicLLMClient(apiKey string, model string, opts map[string]string) *AnthropicLLMClient {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	effort := anthropic.OutputConfigEffort(opts["effort"])
	if effort == "" {
		effort = anthropic.OutputConfigEffortLow
	}

	var thinking anthropic.ThinkingConfigParamUnion
	if budgetStr := opts["thinking"]; budgetStr != "" {
		budget, err := strconv.ParseInt(budgetStr, 10, 64)
		if err == nil && budget >= 1024 {
			thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
		}
	}

	return &AnthropicLLMClient{
		client:   client,
		model:    model,
		effort:   effort,
		thinking: thinking,
	}
}

// Query sends a query to the Anthropic API and returns the structured response
func (c *AnthropicLLMClient) Query(ctx context.Context, queryString string) (*LLMResponse, error) {
	logger := LoggerFromContext(ctx)
	logger.Debug("calling Anthropic API", "model", c.model)

	startTime := time.Now()

	c.history = append(c.history, anthropic.NewUserMessage(anthropic.NewTextBlock(queryString)))

	schema, err := jsonschema.For[LLMResponse](nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate schema: %w", err)
	}

	// Convert the jsonschema output to map[string]any for the Anthropic SDK
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal schema: %w", err)
	}
	var schemaMap map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
	}

	params := anthropic.MessageNewParams{
		Messages:  c.history,
		Model:     anthropic.Model(c.model),
		MaxTokens: 16384,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: c.effort,
			Format: anthropic.JSONOutputFormatParam{
				Schema: schemaMap,
			},
		},
		Thinking: c.thinking,
	}

	message, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("Anthropic API call failed: %w", err)
	}

	duration := time.Since(startTime)

	// Extract text content from response blocks
	var content string
	for _, block := range message.Content {
		if block.Type == "text" {
			content = block.Text
			break
		}
	}
	if content == "" {
		return nil, fmt.Errorf("no text content in Anthropic response")
	}

	logger.Debug("Anthropic API call complete",
		"duration_ms", duration.Milliseconds(),
		"input_tokens", message.Usage.InputTokens,
		"output_tokens", message.Usage.OutputTokens,
	)

	var response LLMResponse
	if err := json.Unmarshal([]byte(content), &response); err != nil {
		return nil, fmt.Errorf("failed to parse Anthropic response: %w", err)
	}

	// Append assistant response to history for multi-turn consistency
	c.history = append(c.history, message.ToParam())

	return &response, nil
}
