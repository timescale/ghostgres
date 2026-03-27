package internal

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type contextKey int

const loggerKey contextKey = iota

// ContextWithLogger adds a logger to the context
func ContextWithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// LoggerFromContext extracts the logger from the context
func LoggerFromContext(ctx context.Context) *zap.Logger {
	return ctx.Value(loggerKey).(*zap.Logger)
}

// LogFormat represents the output format for the logger.
type LogFormat string

const (
	LogFormatConsole LogFormat = "console"
	LogFormatJSON    LogFormat = "json"
)

func (f LogFormat) MarshalText() ([]byte, error) {
	return []byte(f), nil
}

func (f *LogFormat) UnmarshalText(text []byte) error {
	switch s := LogFormat(text); s {
	case LogFormatConsole, LogFormatJSON:
		*f = s
		return nil
	default:
		return fmt.Errorf("invalid log format: %q (must be %q or %q)", s, LogFormatConsole, LogFormatJSON)
	}
}

// NewLogger creates a new zap logger with the given level and format
func NewLogger(level zapcore.Level, format LogFormat) (*zap.Logger, error) {
	levelEncoder := zapcore.CapitalLevelEncoder
	if format == LogFormatConsole {
		levelEncoder = zapcore.CapitalColorLevelEncoder
	}

	loggerConf := zap.Config{
		Level:             zap.NewAtomicLevelAt(level),
		Development:       false,
		DisableCaller:     true,
		DisableStacktrace: true,
		Encoding:          string(format),
		EncoderConfig: zapcore.EncoderConfig{
			MessageKey:     "msg",
			LevelKey:       "level",
			TimeKey:        "ts",
			NameKey:        "name",
			CallerKey:      "caller",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeName:     zapcore.FullNameEncoder,
			EncodeLevel:    levelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}

	return loggerConf.Build()
}
