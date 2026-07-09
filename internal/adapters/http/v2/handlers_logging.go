package v2

import (
	"encoding/json"
	"io"
	"net/http"

	"bticino-go-companion/internal/config"
	"bticino-go-companion/internal/logger"
)

const loggingTag = "adapters.http.logging"

type loggingLevelRequest struct {
	Level string `json:"level"`
}

func (r *Router) handleLogging(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		writeLoggingState(w)
	case http.MethodPut:
		var body loggingLevelRequest
		dec := json.NewDecoder(io.LimitReader(req.Body, 16*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
			return
		}
		level, err := logger.ParseLevel(body.Level)
		if err != nil {
			writeErrorWithExtras(w, http.StatusBadRequest, "invalid_log_level", "invalid log level", map[string]any{"levels": logger.Levels()})
			return
		}
		cfg, err := config.Load(r.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "config_load_failed", "load config failed")
			return
		}
		cfg.LogLevel = level.String()
		if err := config.Save(r.configPath, cfg); err != nil {
			writeError(w, http.StatusInternalServerError, "config_write_failed", "write config failed")
			return
		}
		logger.SetLevel(level)
		logger.Infof(loggingTag, "log level changed level=%s", level.String())
		writeLoggingState(w)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func writeLoggingState(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"level":  logger.GetLevel().String(),
		"levels": logger.Levels(),
		"path":   logger.LogPath(),
	})
}
