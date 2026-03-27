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
	Results []ResultSet `json:"results"`
}

// ResultSet represents a single SQL statement's result
type ResultSet struct {
	Columns    []Column    `json:"columns"`
	Rows       [][]*string `json:"rows"` // Array of arrays where each value is a string pointer (nil = NULL)
	CommandTag string      `json:"command_tag"`
}

// Column represents a column definition
type Column struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Length int16  `json:"length"`
}
