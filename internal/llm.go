package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// System prompt for the LLM
const systemPrompt = `You are a PostgreSQL database that responds to SQL queries. For each query,
return results that are consistent with your previous responses in this session.
Remember what data you've returned before and maintain consistency.

CRITICAL RULES:
1. ALWAYS return results for every query, even if the query seems invalid or malformed
2. NEVER return errors - be creative and interpret the user's intent
3. Return COMPLETE data sets (for example, if the user runs 'SELECT * FROM presidents;', return ALL of the U.S. presidents in order)
4. Only limit results if the query explicitly includes WHERE, LIMIT, or similar filtering clauses
5. The command_tag field must ONLY contain valid success tags like "SELECT 10", "INSERT 0 1", etc. - NEVER error messages

Return your response as structured JSON with a "results" array. Each element represents one
SQL statement's result and contains:
- columns: array of {name, type, length} where type is a PostgreSQL type name and length is -1 for variable-length types
- rows: array of arrays where each value is a string in PostgreSQL text format, or null for NULL values
- command_tag: string like "SELECT 3", "INSERT 0 1", "UPDATE 2", "DELETE 1", "CREATE TABLE", etc.

IMPORTANT: All non-NULL values in rows must be strings, even for numbers. Use PostgreSQL text format.
For example: integer 42 becomes "42", boolean true becomes "t", NULL becomes null (JSON null).

For column types, prefer variable-length types (text, varchar) over fixed-length types when appropriate.
For variable-length types, always use length = -1. For fixed-length types like int4, use the standard size (e.g., 4 for int4).

For multi-statement queries (separated by semicolons), return multiple result sets in the array.

Supported types: text, int4, int8, float8, float4, int2, bool, timestamp, date, uuid, varchar
For NULL values, use JSON null (not the string "NULL").
For non-SELECT queries (INSERT, UPDATE, DELETE, CREATE, etc.), return empty columns and rows arrays,
but always provide an appropriate command_tag.

Be generous with data - if someone asks for a list, give them a reasonably complete list. Make up plausible, realistic
data that fits the query.

Example for "SELECT 1; INSERT INTO foo VALUES (1); SELECT 2;":
{
  "results": [
    {"columns": [{"name": "?column?", "type": "int4", "length": 4}], "rows": [["1"]], "command_tag": "SELECT 1"},
    {"columns": [], "rows": [], "command_tag": "INSERT 0 1"},
    {"columns": [{"name": "?column?", "type": "int4", "length": 4}], "rows": [["2"]], "command_tag": "SELECT 1"}
  ]
}`

// LLMClient handles communication with the OpenAI API
type LLMClient struct {
	client          openai.Client
	model           string
	reasoningEffort string // empty string means use model default
	history         []openai.ChatCompletionMessageParamUnion
}

// NewLLMClient creates a new LLM client with the specified API key, model, and optional reasoning effort
func NewLLMClient(apiKey string, model string, reasoningEffort string) *LLMClient {
	client := openai.NewClient(option.WithAPIKey(apiKey))

	// Initialize chat history with system prompt
	history := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
	}

	return &LLMClient{
		client:          client,
		model:           model,
		reasoningEffort: reasoningEffort,
		history:         history,
	}
}

// Query sends a query to the LLM and returns the structured response
func (c *LLMClient) Query(ctx context.Context, queryString string) (*LLMResponse, error) {
	logger := LoggerFromContext(ctx)
	logger.Debug("calling LLM API", "model", c.model)

	startTime := time.Now()

	// Append user query to chat history
	c.history = append(c.history, openai.UserMessage(queryString))

	// Generate JSON schema from LLMResponse struct
	schema, err := jsonschema.For[LLMResponse](nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate schema: %w", err)
	}

	// Build API request parameters
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

	// Only set ReasoningEffort if explicitly provided
	if c.reasoningEffort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(c.reasoningEffort)
	}

	// Call OpenAI Chat Completions API with structured outputs
	completion, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("LLM API call failed: %w", err)
	}

	duration := time.Since(startTime)

	// Extract the response content
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("no choices in LLM response")
	}

	content := completion.Choices[0].Message.Content
	logger.Debug("LLM API call complete",
		"duration_ms", duration.Milliseconds(),
		"prompt_tokens", completion.Usage.PromptTokens,
		"completion_tokens", completion.Usage.CompletionTokens,
	)

	// Parse the response into LLMResponse struct
	var response LLMResponse
	if err := json.Unmarshal([]byte(content), &response); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Append assistant response to chat history
	c.history = append(c.history, openai.AssistantMessage(content))

	return &response, nil
}
