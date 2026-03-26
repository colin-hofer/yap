package deploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

func NewLogger(cfg *Config, component string) *slog.Logger {
	level := parseLevel(cfg.LogLevel)

	if strings.EqualFold(cfg.LogFormat, "json") {
		h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
		return slog.New(h).With(
			slog.String("app", cfg.AppName),
			slog.String("component", component),
		)
	}

	h := NewPrettyHandler(os.Stdout, level)
	return slog.New(h).With(
		slog.String("app", cfg.AppName),
		slog.String("component", component),
	)
}

type PrettyHandler struct {
	w     io.Writer
	level slog.Level
	color bool

	mu    *sync.Mutex
	attrs []slog.Attr
	group string
}

func NewPrettyHandler(w io.Writer, level slog.Level) *PrettyHandler {
	return &PrettyHandler{
		w:     w,
		level: level,
		color: shouldUseColor(),
		mu:    &sync.Mutex{},
	}
}

func (h *PrettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	line := h.render(r)
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line)
	return err
}

func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := h.clone()
	next.attrs = append(next.attrs, attrs...)
	return next
}

func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	next := h.clone()
	if next.group == "" {
		next.group = name
	} else {
		next.group = next.group + "." + name
	}
	return next
}

func (h *PrettyHandler) clone() *PrettyHandler {
	attrs := make([]slog.Attr, len(h.attrs))
	copy(attrs, h.attrs)
	return &PrettyHandler{
		w:     h.w,
		level: h.level,
		color: h.color,
		mu:    h.mu,
		attrs: attrs,
		group: h.group,
	}
}

func (h *PrettyHandler) render(r slog.Record) string {
	var b strings.Builder

	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	b.WriteString(h.paint(dim, ts.UTC().Format("15:04:05")))
	b.WriteString(" ")
	b.WriteString(h.levelLabel(r.Level))
	b.WriteString(" ")
	b.WriteString(h.paint(bold, r.Message))

	attrs := make([]slog.Attr, 0, len(h.attrs)+6)
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		if a.Key == "" {
			continue
		}
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		b.WriteString("  ")
		b.WriteString(h.paint(keyColor, key))
		b.WriteString("=")
		b.WriteString(formatValue(a.Value))
	}

	b.WriteString("\n")
	return b.String()
}

func (h *PrettyHandler) levelLabel(level slog.Level) string {
	s := strings.ToUpper(level.String())
	if idx := strings.IndexByte(s, '+'); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		s = s[:idx]
	}
	s = padRight(s, 5)

	switch {
	case level >= slog.LevelError:
		return h.paint(red, s)
	case level >= slog.LevelWarn:
		return h.paint(yellow, s)
	case level >= slog.LevelInfo:
		return h.paint(green, s)
	default:
		return h.paint(cyan, s)
	}
}

func (h *PrettyHandler) paint(code, s string) string {
	if !h.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func parseLevel(v string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func formatValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return quoteIfNeeded(v.String())
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().UTC().Format(time.RFC3339)
	case slog.KindBool:
		if v.Bool() {
			return "true"
		}
		return "false"
	case slog.KindInt64:
		return fmt.Sprintf("%d", v.Int64())
	case slog.KindUint64:
		return fmt.Sprintf("%d", v.Uint64())
	case slog.KindFloat64:
		return fmt.Sprintf("%g", v.Float64())
	default:
		return quoteIfNeeded(v.String())
	}
}

func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\"") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func shouldUseColor() bool {
	if strings.TrimSpace(os.Getenv("NO_COLOR")) != "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

const (
	bold     = "1"
	dim      = "2"
	red      = "31"
	yellow   = "33"
	green    = "32"
	cyan     = "36"
	keyColor = "38;5;110"
)
