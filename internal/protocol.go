package internal

import (
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
)

// typeMap is a package-level pgtype.Map instance for type lookups
var typeMap = pgtype.NewMap()

// typeNameToOID converts a PostgreSQL type name to its OID
// Falls back to text (OID 25) for unknown types
func typeNameToOID(typeName string) uint32 {
	typ, ok := typeMap.TypeForName(typeName)
	if !ok {
		// Fallback to text for unknown types
		typ, _ = typeMap.TypeForName("text")
	}
	return typ.OID
}

// buildRowDescription creates a RowDescription message from columns
func buildRowDescription(columns []Column) *pgproto3.RowDescription {
	fields := make([]pgproto3.FieldDescription, len(columns))
	for i, col := range columns {
		fields[i] = pgproto3.FieldDescription{
			Name:                 []byte(col.Name),
			TableOID:             0,
			TableAttributeNumber: 0,
			DataTypeOID:          typeNameToOID(col.Type),
			DataTypeSize:         int16(col.Length),
			TypeModifier:         -1,
			Format:               0, // text format
		}
	}
	return &pgproto3.RowDescription{Fields: fields}
}

// buildDataRow creates a DataRow message from a row of values
func buildDataRow(row []*string) *pgproto3.DataRow {
	values := make([][]byte, len(row))
	for i, val := range row {
		if val == nil {
			values[i] = nil // NULL
		} else {
			values[i] = []byte(*val)
		}
	}
	return &pgproto3.DataRow{Values: values}
}

// buildErrorResponse creates an ErrorResponse message
func buildErrorResponse(severity, code, message string) *pgproto3.ErrorResponse {
	return &pgproto3.ErrorResponse{
		Severity: severity,
		Code:     code,
		Message:  message,
	}
}
