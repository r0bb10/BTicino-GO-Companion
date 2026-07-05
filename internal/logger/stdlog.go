package logger

import (
	"io"
	"log"
	"strings"

	"github.com/pion/logging"
)

type levelWriter struct {
	minLevel Level
	tag      string
}

func (w *levelWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if msg != "" {
		logf(w.minLevel, w.tag, "%s", msg)
	}
	return len(p), nil
}

func StdLogger(minLevel Level) *log.Logger {
	return StdTaggedLogger("stdlog", minLevel)

}

func StdTaggedLogger(tag string, minLevel Level) *log.Logger {
	if strings.TrimSpace(tag) == "" {
		tag = "stdlog"
	}
	return log.New(&levelWriter{minLevel: minLevel, tag: tag}, "", 0)
}

type pionLogger struct {
	tag string
}

func (l *pionLogger) Trace(msg string) {
	Debugf(l.tag, "%s", msg)
}

func (l *pionLogger) Tracef(format string, args ...any) {
	Debugf(l.tag, format, args...)
}

func (l *pionLogger) Debug(msg string) {
	Debugf(l.tag, "%s", msg)
}

func (l *pionLogger) Debugf(format string, args ...any) {
	Debugf(l.tag, format, args...)
}

func (l *pionLogger) Info(msg string) {
	Infof(l.tag, "%s", msg)
}

func (l *pionLogger) Infof(format string, args ...any) {
	Infof(l.tag, format, args...)
}

func (l *pionLogger) Warn(msg string) {
	Warnf(l.tag, "%s", msg)
}

func (l *pionLogger) Warnf(format string, args ...any) {
	Warnf(l.tag, format, args...)
}

func (l *pionLogger) Error(msg string) {
	Errorf(l.tag, "%s", msg)
}

func (l *pionLogger) Errorf(format string, args ...any) {
	Errorf(l.tag, format, args...)
}

var _ logging.LeveledLogger = (*pionLogger)(nil)

type pionFactory struct{}

func (f *pionFactory) NewLogger(scope string) logging.LeveledLogger {
	return &pionLogger{tag: "pion." + scope}
}

func NewPionLoggerFactory() logging.LoggerFactory {
	return &pionFactory{}
}

var _ io.Writer = (*levelWriter)(nil)
