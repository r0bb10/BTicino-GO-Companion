package v2

import (
	"errors"
	"io"
	"net/http"
	"os"
)

const maxLogResponseBytes = 512 * 1024

var companionLogPath = "/tmp/companion.log"

func (r *Router) handleLogs(w http.ResponseWriter, req *http.Request) {
	if !requireMethod(w, req, http.MethodGet) {
		return
	}
	b, err := readLogTail(companionLogPath, maxLogResponseBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{
				"log":     "",
				"missing": true,
				"path":    companionLogPath,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "log_read_failed", "read companion log failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"log":        string(b),
		"path":       companionLogPath,
		"tail_bytes": maxLogResponseBytes,
	})
}

func readLogTail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if info.Size() > maxBytes {
		start = info.Size() - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
