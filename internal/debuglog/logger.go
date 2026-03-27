package debuglog

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const fileName = "debug.log"

var (
	mu      sync.RWMutex
	logger  *slog.Logger
	logFile *os.File
	logPath string
)

// Enable opens the debug log file under the given root and enables structured logging.
func Enable(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("debug log root cannot be empty")
	}

	path := filepath.Join(root, fileName)

	mu.Lock()
	defer mu.Unlock()

	if logFile != nil {
		if logPath == path {
			return logPath, nil
		}
		_ = closeLocked()
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create debug log dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("open debug log: %w", err)
	}

	handler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger = slog.New(handler).With(slog.String("component", "yap"))
	logFile = f
	logPath = path
	logger.Info("debug logging enabled", slog.String("path", path))
	return path, nil
}

// Close flushes and closes the debug log file.
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	return closeLocked()
}

func closeLocked() error {
	if logFile == nil {
		logger = nil
		logPath = ""
		return nil
	}

	currentLogger := logger
	currentFile := logFile
	currentPath := logPath

	logger = nil
	logFile = nil
	logPath = ""

	if currentLogger != nil {
		currentLogger.Info("debug logging disabled", slog.String("path", currentPath))
	}
	return currentFile.Close()
}

// Enabled reports whether debug logging is currently active.
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return logger != nil
}

// Path returns the active debug log path, if any.
func Path() string {
	mu.RLock()
	defer mu.RUnlock()
	return logPath
}

// Debug logs a debug-level message when debugging is enabled.
func Debug(msg string, args ...any) {
	log(slog.LevelDebug, msg, args...)
}

// Info logs an info-level message when debugging is enabled.
func Info(msg string, args ...any) {
	log(slog.LevelInfo, msg, args...)
}

// Warn logs a warning-level message when debugging is enabled.
func Warn(msg string, args ...any) {
	log(slog.LevelWarn, msg, args...)
}

// Error logs an error-level message when debugging is enabled.
func Error(msg string, args ...any) {
	log(slog.LevelError, msg, args...)
}

func log(level slog.Level, msg string, args ...any) {
	mu.RLock()
	current := logger
	mu.RUnlock()
	if current == nil {
		return
	}
	current.Log(context.Background(), level, msg, args...)
}
