package log

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/gammadia/alfred/server/flags"
	"github.com/spf13/viper"
)

// For some reason, gopls imports a bad package when using a package-global variable 'log'
// Let's move it to an actual package so that it doesn't get confused...

// Base is a bare logger without attributes
var Base *slog.Logger

// logger is the server logger with default attributes
var logger *slog.Logger

func Init() error {
	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(viper.GetString(flags.LogLevel))); err != nil {
		return fmt.Errorf("failed to parse log level: %w", err)
	}

	options := slog.HandlerOptions{
		AddSource: viper.GetBool(flags.LogSource),
		Level:     logLevel,
	}

	switch format := viper.GetString(flags.LogFormat); format {
	case "json":
		Base = slog.New(slog.NewJSONHandler(os.Stdout, &options))
	case "text":
		Base = slog.New(slog.NewTextHandler(os.Stdout, &options))
	default:
		return fmt.Errorf("unknown log format '%s'", format)
	}

	logger = Base.With("component", "server")
	return nil
}

// Proxies for slog.Logger methods

func Debug(msg string, args ...any) {
	logger.Debug(msg, args...)
}

func DebugContext(ctx context.Context, msg string, args ...any) {
	logger.DebugContext(ctx, msg, args...)
}

func Info(msg string, args ...any) {
	logger.Info(msg, args...)
}

func InfoContext(ctx context.Context, msg string, args ...any) {
	logger.InfoContext(ctx, msg, args...)
}

func Warn(msg string, args ...any) {
	logger.Warn(msg, args...)
}

func WarnContext(ctx context.Context, msg string, args ...any) {
	logger.WarnContext(ctx, msg, args...)
}

func Error(msg string, args ...any) {
	logger.Error(msg, args...)
}

func ErrorContext(ctx context.Context, msg string, args ...any) {
	logger.ErrorContext(ctx, msg, args...)
}

func With(args ...any) *slog.Logger {
	return logger.With(args...)
}
