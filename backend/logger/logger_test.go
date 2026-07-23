package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeLogWritesAndDeletesExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	writer := &dailyWriter{dir: dir}
	if _, err := writer.Write([]byte("runtime-log-test\n")); err != nil {
		t.Fatalf("write runtime log: %v", err)
	}
	if err := writer.file.Close(); err != nil {
		t.Fatalf("close runtime log: %v", err)
	}
	runtimeLogDir = dir

	today := time.Now().Format("2006-01-02")
	content, err := os.ReadFile(filepath.Join(dir, "relaydeck-"+today+".log"))
	if err != nil {
		t.Fatalf("read runtime log: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("runtime log is empty")
	}

	old := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	oldPath := filepath.Join(dir, "relaydeck-"+old+".log")
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	deleted, err := DeleteRuntimeLogsBefore(time.Now().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("delete runtime logs: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old log still exists: %v", err)
	}
}
