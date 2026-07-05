package logger

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

const DefaultLogPath = "/tmp/companion.log"

var (
	globalLevel  Level     = INFO
	globalOutput io.Writer = os.Stderr
	logPath                = DefaultLogPath
	mu           sync.RWMutex
)

func ParseLevel(raw string) (Level, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	switch trimmed {
	case "debug", "d":
		return DEBUG, nil
	case "info", "i":
		return INFO, nil
	case "warn", "warning", "w":
		return WARN, nil
	case "error", "err", "e":
		return ERROR, nil
	default:
		return INFO, fmt.Errorf("unknown log level %q", raw)
	}
}

func Levels() []string {
	return []string{DEBUG.String(), INFO.String(), WARN.String(), ERROR.String()}
}

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "debug"
	case INFO:
		return "info"
	case WARN:
		return "warn"
	case ERROR:
		return "error"
	default:
		return "info"
	}
}

func (l Level) Char() byte {
	switch l {
	case DEBUG:
		return 'D'
	case INFO:
		return 'I'
	case WARN:
		return 'W'
	case ERROR:
		return 'E'
	default:
		return 'I'
	}
}

func SetLevel(l Level) {
	mu.Lock()
	globalLevel = l
	mu.Unlock()
}

func GetLevel() Level {
	mu.RLock()
	defer mu.RUnlock()
	return globalLevel
}

func SetOutput(w io.Writer) {
	mu.Lock()
	if w == nil {
		w = os.Stderr
	}
	globalOutput = w
	mu.Unlock()
}

func GetOutput() io.Writer {
	mu.RLock()
	defer mu.RUnlock()
	return globalOutput
}

func SetLogPath(path string) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = DefaultLogPath
	}
	mu.Lock()
	logPath = trimmed
	mu.Unlock()
}

func LogPath() string {
	mu.RLock()
	defer mu.RUnlock()
	return logPath
}

func InitFile(path string, mirrorStderr bool) (func() error, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = DefaultLogPath
	}
	f, err := os.OpenFile(trimmed, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	var out io.Writer = f
	if mirrorStderr {
		out = io.MultiWriter(f, os.Stderr)
	}
	mu.Lock()
	logPath = trimmed
	globalOutput = out
	globalLevel = INFO
	mu.Unlock()
	return f.Close, nil
}

func levelEnabled(l Level) bool {
	mu.RLock()
	defer mu.RUnlock()
	return l >= globalLevel
}

func logf(l Level, tag, format string, args ...any) {
	now := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf(format, args...)
	mu.Lock()
	defer mu.Unlock()
	if l < globalLevel {
		return
	}
	if globalOutput == nil {
		globalOutput = os.Stderr
	}
	fmt.Fprintf(globalOutput, "%s [%c] [%s] %s\n", now, l.Char(), tag, msg)
}

func Debugf(tag, format string, args ...any) {
	logf(DEBUG, tag, format, args...)
}

func Infof(tag, format string, args ...any) {
	logf(INFO, tag, format, args...)
}

func Warnf(tag, format string, args ...any) {
	logf(WARN, tag, format, args...)
}

func Errorf(tag, format string, args ...any) {
	logf(ERROR, tag, format, args...)
}
