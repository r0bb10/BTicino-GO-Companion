package logger

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preserveGlobals(t *testing.T) {
	t.Helper()
	level := GetLevel()
	output := GetOutput()
	path := LogPath()
	t.Cleanup(func() {
		SetLevel(level)
		SetOutput(output)
		SetLogPath(path)
	})
}

func TestLevels(t *testing.T) {
	preserveGlobals(t)
	SetLevel(INFO)
	if GetLevel() != INFO {
		t.Fatalf("expected INFO, got %v", GetLevel())
	}
	if levelEnabled(DEBUG) {
		t.Fatal("DEBUG should be disabled at INFO level")
	}
	if !levelEnabled(INFO) {
		t.Fatal("INFO should be enabled at INFO level")
	}
	if !levelEnabled(WARN) {
		t.Fatal("WARN should be enabled at INFO level")
	}
	if !levelEnabled(ERROR) {
		t.Fatal("ERROR should be enabled at INFO level")
	}
}

func TestOutput(t *testing.T) {
	preserveGlobals(t)
	var buf bytes.Buffer
	SetOutput(&buf)
	SetLevel(DEBUG)

	Infof("test", "hello %s", "world")
	Debugf("test", "debug msg")
	Warnf("test", "warn msg")
	Errorf("test", "error msg")

	out := buf.String()
	if !strings.Contains(out, "[I]") {
		t.Fatal("missing INFO")
	}
	if !strings.Contains(out, "[D]") {
		t.Fatal("missing DEBUG")
	}
	if !strings.Contains(out, "[W]") {
		t.Fatal("missing WARN")
	}
	if !strings.Contains(out, "[E]") {
		t.Fatal("missing ERROR")
	}
	if !strings.Contains(out, "[test]") {
		t.Fatal("missing tag")
	}
	if !strings.Contains(out, "hello world") {
		t.Fatal("missing message")
	}
}

func TestLevelFiltering(t *testing.T) {
	preserveGlobals(t)
	var buf bytes.Buffer
	SetOutput(&buf)
	SetLevel(WARN)

	Infof("test", "should be hidden")
	Warnf("test", "should appear")

	out := buf.String()
	if strings.Contains(out, "[I]") {
		t.Fatal("INFO should be hidden at WARN level")
	}
	if !strings.Contains(out, "[W]") {
		t.Fatal("WARN should appear")
	}

	SetLevel(DEBUG)
	Infof("test", "now visible")
	out = buf.String()
	if !strings.Contains(out, "now visible") {
		t.Fatal("INFO should be visible at DEBUG level")
	}
}

func TestStdLoggerBridge(t *testing.T) {
	preserveGlobals(t)
	var buf bytes.Buffer
	SetOutput(&buf)
	SetLevel(INFO)

	std := StdLogger(INFO)
	std.Println("bridge message")

	out := buf.String()
	if !strings.Contains(out, "bridge message") {
		t.Fatal("StdLogger bridge should pass messages at INFO level")
	}

	SetLevel(ERROR)
	std.Println("should be hidden")
	out2 := buf.String()
	if strings.Contains(out2, "should be hidden") {
		t.Fatal("StdLogger should suppress INFO bridge at ERROR global level")
	}
}

func TestParseLevel(t *testing.T) {
	level, err := ParseLevel("debug")
	if err != nil || level != DEBUG {
		t.Fatalf("expected debug, got %v err=%v", level, err)
	}
	level, err = ParseLevel("WARNING")
	if err != nil || level != WARN {
		t.Fatalf("expected warn, got %v err=%v", level, err)
	}
	if _, err := ParseLevel("nope"); err == nil {
		t.Fatal("expected invalid level error")
	}
	if _, err := ParseLevel(""); err == nil {
		t.Fatal("expected blank level error")
	}
}

func TestInitFileTruncatesAndWrites(t *testing.T) {
	preserveGlobals(t)
	path := filepath.Join(t.TempDir(), "companion.log")
	if err := os.WriteFile(path, []byte("old log\n"), 0o644); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	closeLog, err := InitFile(path, false)
	if err != nil {
		t.Fatalf("init file: %v", err)
	}
	Infof("test", "new log")
	if err := closeLog(); err != nil {
		t.Fatalf("close log: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	out := string(b)
	if strings.Contains(out, "old log") {
		t.Fatalf("expected log to be truncated, got %q", out)
	}
	if !strings.Contains(out, "new log") {
		t.Fatalf("expected new log, got %q", out)
	}
}
