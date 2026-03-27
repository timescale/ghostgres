package internal

import (
	"context"
	_ "embed"
)

// DefaultSystemPrompt is used when no custom --prompt file is provided.
//
//go:embed prompt.md
var DefaultSystemPrompt string

// LLMClient is the interface for all LLM provider clients.
type LLMClient interface {
	Query(ctx context.Context, queryString string) (*LLMResponse, error)
}

// LLMResponse represents the structured output from the LLM
type LLMResponse struct {
	Results []ResultSet `json:"results" jsonschema:"array of result sets, one per SQL statement"`
}

// ResultSet represents a single SQL statement's result
type ResultSet struct {
	Columns    []Column    `json:"columns" jsonschema:"column definitions; empty for non-SELECT statements"`
	Rows       [][]*string `json:"rows" jsonschema:"array of rows; each row is an array of string values in PostgreSQL text format (e.g. 42 becomes \"42\", true becomes \"t\") or null for NULL; empty for non-SELECT statements"`
	CommandTag string      `json:"command_tag" jsonschema:"PostgreSQL command tag like SELECT 3, INSERT 0 1, UPDATE 2, DELETE 1, CREATE TABLE"`
}

// Column represents a column definition
type Column struct {
	Name   string `json:"name" jsonschema:"column name"`
	Type   string `json:"type" jsonschema:"PostgreSQL type name: text, int4, int8, float8, float4, int2, bool, timestamp, date, uuid, varchar"`
	Length int16  `json:"length" jsonschema:"type length in bytes; use -1 for variable-length types (text, varchar), standard size for fixed (e.g. 4 for int4)"`
}
