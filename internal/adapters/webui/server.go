package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"bticino-go-companion/internal/adapters/openwebnet"
	"bticino-go-companion/internal/auth"
	"bticino-go-companion/internal/config"
	"bticino-go-companion/internal/logger"
	"bticino-go-companion/internal/services/diagnostics"
	"bticino-go-companion/internal/services/runtime"
	"bticino-go-companion/internal/system"

	"golang.org/x/crypto/bcrypt"
)

const (
	defaultUsername = "companion"
	defaultPassword = "companion"
	loggingTag      = "adapters.webui.logging"
	sessionCookie   = "companion_web_session"
	maxConfigBytes  = 512 * 1024
	maxLogBytes     = 512 * 1024
)

type UpdateStatusInfo struct {
	Stage     string `json:"stage"`
	Available string `json:"available"`
}

type Options struct {
	ConfigPath   string
	LogPath      string
	AuthStore    *auth.Store
	Runtime      RuntimeDeviceInfo
	Status       *runtime.Status
	Diagnostics  *diagnostics.Service
	FrameBuffer  *openwebnet.FrameBuffer
	UpdateStatus func() UpdateStatusInfo
	BootTime     time.Time
	RestartFunc  func(script string) error
}

type RuntimeDeviceInfo struct {
	Model    string
	Firmware string
	Hardware string
}

type Server struct {
	configPath   string
	logPath      string
	authStore    *auth.Store
	runtime      RuntimeDeviceInfo
	status       *runtime.Status
	diagnostics  *diagnostics.Service
	frameBuffer  *openwebnet.FrameBuffer
	updateStatus func() UpdateStatusInfo
	bootTime     time.Time
	restartFunc  func(script string) error

	mu       sync.Mutex
	sessions map[string]session
}

type session struct {
	Bootstrap     bool
	SessionSecret string
	CreatedAt     time.Time
}

func New(opts Options) *Server {
	logPath := strings.TrimSpace(opts.LogPath)
	if logPath == "" {
		logPath = logger.DefaultLogPath
	}
	bootTime := opts.BootTime
	if bootTime.IsZero() {
		bootTime = time.Now()
	}
	return &Server{
		configPath:  strings.TrimSpace(opts.ConfigPath),
		logPath:     logPath,
		authStore:   opts.AuthStore,
		status:      opts.Status,
		diagnostics: opts.Diagnostics,
		runtime: RuntimeDeviceInfo{
			Model:    strings.TrimSpace(opts.Runtime.Model),
			Firmware: strings.TrimSpace(opts.Runtime.Firmware),
			Hardware: strings.TrimSpace(opts.Runtime.Hardware),
		},
		frameBuffer:  opts.FrameBuffer,
		updateStatus: opts.UpdateStatus,
		bootTime:     bootTime,
		restartFunc:  opts.RestartFunc,
		sessions:     make(map[string]session),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session", s.handleSession)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/credentials", s.handleCredentials)
	mux.HandleFunc("/api/status", s.requireReady(s.handleStatus))
	mux.HandleFunc("/api/config", s.requireReady(s.handleConfig))
	mux.HandleFunc("/api/logs", s.requireReady(s.handleLogs))
	mux.HandleFunc("/api/logging", s.requireReady(s.handleLogging))
	mux.HandleFunc("/api/frames", s.requireReady(s.handleFrames))
	mux.HandleFunc("/api/restart", s.requireReady(s.handleRestart))
	mux.Handle("/", s.staticHandler())
	return mux
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load config failed")
		return
	}
	sess, ok := s.currentSession(r, cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":      ok,
		"bootstrap":          ok && sess.Bootstrap,
		"bootstrap_required": !configuredAuth(cfg),
		"username":           cfg.WebAuth.Username,
		"version":            cfg.Version,
		"git_sha":            cfg.GitSHA,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load config failed")
		return
	}
	bootstrap := !configuredAuth(cfg)
	if bootstrap {
		if req.Username != defaultUsername || req.Password != defaultPassword {
			writeError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
	} else if !checkConfiguredPassword(cfg, req.Username, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := randomHex(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create session failed")
		return
	}
	s.mu.Lock()
	s.sessions[token] = session{Bootstrap: bootstrap, SessionSecret: cfg.WebAuth.SessionSecret, CreatedAt: time.Now()}
	s.mu.Unlock()
	setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bootstrap": bootstrap})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req credentialsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load config failed")
		return
	}
	sess, ok := s.currentSession(r, cfg)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || strings.ContainsAny(username, "\r\n\t") {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if sess.Bootstrap {
		if username == defaultUsername || req.Password == defaultPassword {
			writeError(w, http.StatusBadRequest, "replace the default username and password")
			return
		}
	} else if !checkConfiguredPassword(cfg, cfg.WebAuth.Username, req.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "current password is invalid")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash password failed")
		return
	}
	secret, err := randomHex(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create session secret failed")
		return
	}
	cfg.WebAuth = config.WebAuthConfig{
		Enabled:       true,
		Username:      username,
		PasswordHash:  string(hash),
		SessionSecret: secret,
	}
	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "save config failed")
		return
	}
	s.mu.Lock()
	s.sessions = make(map[string]session)
	s.mu.Unlock()
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load config failed")
		return
	}
	model := normalizedOrFallback(cfg.DeviceModel, s.runtime.Model)
	firmware := normalizedOrFallback(cfg.DeviceFirmware, s.runtime.Firmware)
	firmware = normalizedOrFallback(firmware, system.DetectLocalMetadata().Firmware)
	hardware := normalizedOrFallback(cfg.DeviceHardware, s.runtime.Hardware)
	hardware = normalizedOrFallback(hardware, hardwareVersion(cfg))
	updateInfo := UpdateStatusInfo{Stage: "checking"}
	if s.updateStatus != nil {
		updateInfo = s.updateStatus()
	}
	network := diagnostics.NetworkSnapshot{}
	if s.diagnostics != nil {
		network = s.diagnostics.NetworkSnapshot()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model":          model,
		"firmware":       firmware,
		"hardware":       hardware,
		"version":        cfg.Version,
		"git_sha":        cfg.GitSHA,
		"ha_paired":      cfg.Auth.Claimed,
		"uptime_seconds": int64(time.Since(s.bootTime).Seconds()),
		"free_ram_kb":    readMemAvailableKB(),
		"wifi_strength":  network.WiFiStrength,
		"update_status":  updateInfo,
	})
}

func readMemAvailableKB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			value, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				return value
			}
		}
	}
	return 0
}

func hardwareVersion(cfg config.Config) string {
	distribution := strings.TrimSpace(cfg.DeviceDistribution)
	if distribution != "" && !strings.EqualFold(distribution, "unknown") {
		return distribution
	}
	return strings.TrimSpace(cfg.DeviceHardware)
}

func normalizedOrFallback(primary string, fallback string) string {
	first := strings.TrimSpace(primary)
	if first != "" && !strings.EqualFold(first, "unknown") {
		return first
	}
	second := strings.TrimSpace(fallback)
	if second != "" && !strings.EqualFold(second, "unknown") {
		return second
	}
	return "unknown"
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b, err := os.ReadFile(s.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read config failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": string(b)})
	case http.MethodPut:
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "config is too large")
			return
		}
		var persisted config.PersistedConfig
		if err := json.Unmarshal(b, &persisted); err != nil {
			writeError(w, http.StatusBadRequest, "config must be valid companion JSON")
			return
		}
		if err := os.WriteFile(s.configPath, append(b, '\n'), 0o644); err != nil {
			writeError(w, http.StatusInternalServerError, "write config failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	b, err := readTail(s.logPath, maxLogBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"log": "", "missing": true, "path": s.logPath})
			return
		}
		writeError(w, http.StatusInternalServerError, "read log failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": string(b), "path": s.logPath})
}

func (s *Server) handleLogging(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeLoggingState(w)
	case http.MethodPut:
		var req struct {
			Level string `json:"level"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		level, err := logger.ParseLevel(req.Level)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid log level", "levels": logger.Levels()})
			return
		}
		logger.SetLevel(level)
		logger.Infof(loggingTag, "log level changed level=%s", level.String())
		s.writeLoggingState(w)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleFrames(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.frameBuffer == nil {
		writeJSON(w, http.StatusOK, map[string]any{"frames": []any{}, "active": false})
		return
	}
	frames := s.frameBuffer.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"frames": frames, "active": true})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load config failed")
		return
	}
	script := strings.TrimSpace(cfg.UpdateServiceScript)
	if script == "" {
		script = "/etc/init.d/companion"
	}
	restart := s.restartFunc
	if restart == nil {
		restart = restartCompanionService
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		if err := restart(script); err != nil {
			logger.Errorf("adapters.webui.restart", "restart failed script=%s err=%v", script, err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "restarting": true})
}

func restartCompanionService(script string) error {
	return exec.Command(script, "restart").Start()
}

func (s *Server) writeLoggingState(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"level":  logger.GetLevel().String(),
		"levels": logger.Levels(),
		"path":   s.logPath,
	})
}

func (s *Server) requireReady(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(s.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "load config failed")
			return
		}
		sess, ok := s.currentSession(r, cfg)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if sess.Bootstrap {
			writeError(w, http.StatusForbidden, "credential setup required")
			return
		}
		next(w, r)
	}
}

func (s *Server) currentSession(r *http.Request, cfg config.Config) (session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return session{}, false
	}
	s.mu.Lock()
	sess, ok := s.sessions[cookie.Value]
	s.mu.Unlock()
	if !ok {
		return session{}, false
	}
	if sess.Bootstrap {
		return sess, !configuredAuth(cfg)
	}
	if cfg.WebAuth.SessionSecret == "" || subtle.ConstantTimeCompare([]byte(sess.SessionSecret), []byte(cfg.WebAuth.SessionSecret)) != 1 {
		return session{}, false
	}
	return sess, true
}

func configuredAuth(cfg config.Config) bool {
	return strings.TrimSpace(cfg.WebAuth.Username) != "" && strings.TrimSpace(cfg.WebAuth.PasswordHash) != ""
}

func checkConfiguredPassword(cfg config.Config, username string, password string) bool {
	if !configuredAuth(cfg) || strings.TrimSpace(username) != cfg.WebAuth.Username {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(cfg.WebAuth.PasswordHash), []byte(password)) == nil
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type credentialsRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	CurrentPassword string `json:"current_password"`
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func randomHex(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func readTail(path string, maxBytes int64) ([]byte, error) {
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

func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(fmt.Sprintf("webui static files: %v", err))
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if r.URL.Path == "/" {
			b, err := staticFiles.ReadFile("static/index.html")
			if err != nil {
				writeError(w, http.StatusInternalServerError, "static file not found")
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		files.ServeHTTP(w, r)
	})
}
