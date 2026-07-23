package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var runtimeLogDir string

type dailyWriter struct {
	dir  string
	mu   sync.Mutex
	date string
	file *os.File
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if w.file == nil || w.date != today {
		if w.file != nil {
			_ = w.file.Close()
		}
		if err := os.MkdirAll(w.dir, 0o755); err != nil {
			return 0, err
		}
		file, err := os.OpenFile(filepath.Join(w.dir, "relaydeck-"+today+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		w.file = file
		w.date = today
	}
	return w.file.Write(p)
}

func New(level, format, logDir string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}

	output := io.Writer(os.Stdout)
	if strings.TrimSpace(logDir) != "" {
		runtimeLogDir = filepath.Clean(logDir)
		output = io.MultiWriter(os.Stdout, &dailyWriter{dir: runtimeLogDir})
	}

	var h slog.Handler
	if strings.ToLower(format) == "json" {
		h = slog.NewJSONHandler(output, opts)
	} else {
		h = slog.NewTextHandler(output, opts)
	}
	return slog.New(h)
}

func DeleteRuntimeLogsBefore(cutoff time.Time) (int, error) {
	if runtimeLogDir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(runtimeLogDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	deleted := 0
	cutoffDate := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "relaydeck-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSuffix(strings.TrimPrefix(name, "relaydeck-"), ".log"), time.Local)
		if err != nil || !parsed.Before(cutoffDate) {
			continue
		}
		if err := os.Remove(filepath.Join(runtimeLogDir, name)); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}
