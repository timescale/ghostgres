package internal

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgproto3"
)

// handleQuery processes a query and sends the response
func handleQuery(ctx context.Context, backend *pgproto3.Backend, llmClient LLMClient, queryString string) error {
	logger := LoggerFromContext(ctx)
	logger.Info("query received")

	// Call LLM to get response
	response, err := llmClient.Query(ctx, queryString)
	if err != nil {
		logger.Error("LLM query failed", "error", err)
		backend.Send(buildErrorResponse("ERROR", "XX000", fmt.Sprintf("LLM API error: %v", err)))
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		backend.Flush()
		return fmt.Errorf("LLM query failed: %w", err)
	}

	// Debug log the response structure
	for i, resultSet := range response.Results {
		columnNames := make([]string, len(resultSet.Columns))
		for j, col := range resultSet.Columns {
			columnNames[j] = col.Name
		}
		logger.Debug("result set",
			"index", i,
			"columns", columnNames,
			"row_count", len(resultSet.Rows),
			"command_tag", resultSet.CommandTag)

		// Log each row
		for rowIdx, row := range resultSet.Rows {
			rowValues := make([]string, len(row))
			for colIdx, val := range row {
				if val == nil {
					rowValues[colIdx] = "NULL"
				} else {
					rowValues[colIdx] = *val
				}
			}
			logger.Debug("row data", "result_set", i, "row", rowIdx, "values", rowValues)
		}
	}

	logger.Info("sending query results")

	// Process each result set
	for _, resultSet := range response.Results {
		// Send RowDescription if columns exist
		if len(resultSet.Columns) > 0 {
			backend.Send(buildRowDescription(resultSet.Columns))

			// Send DataRow for each row
			for _, row := range resultSet.Rows {
				backend.Send(buildDataRow(row))
			}
		}

		// Send CommandComplete
		backend.Send(&pgproto3.CommandComplete{
			CommandTag: []byte(resultSet.CommandTag),
		})
	}

	// Send ReadyForQuery
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})

	// Flush all messages
	if err := backend.Flush(); err != nil {
		// Send error response
		backend.Send(buildErrorResponse("ERROR", "XX000", fmt.Sprintf("failed to send response: %v", err)))
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		backend.Flush()
		return fmt.Errorf("failed to flush query response: %w", err)
	}

	logger.Info("query complete")
	return nil
}
