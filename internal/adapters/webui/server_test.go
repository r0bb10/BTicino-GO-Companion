package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bticino-go-companion/internal/config"
	"bticino-go-companion/internal/logger"
)

func TestBootstrapLoginCannotAccessConfigUntilCredentialsAreSet(t *testing.T) {
	configPath, logPath := writeTestFiles(t)
	srv := httptest.NewServer(New(Options{ConfigPath: configPath, LogPath: logPath}).Handler())
	t.Cleanup(srv.Close)

	resp := doJSON(t, srv.URL+"/api/config", http.MethodGet, nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated config status 401, got %d", resp.StatusCode)
	}

	loginResp := doJSON(t, srv.URL+"/api/login", http.MethodPost, map[string]string{"username": "companion", "password": "companion"}, nil)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected default bootstrap login status 200, got %d body=%s", loginResp.StatusCode, readBody(t, loginResp))
	}
	bootstrapCookie := sessionFromResponse(t, loginResp)

	resp = doJSON(t, srv.URL+"/api/config", http.MethodGet, nil, bootstrapCookie)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected bootstrap config status 403, got %d", resp.StatusCode)
	}

	setupResp := doJSON(t, srv.URL+"/api/credentials", http.MethodPost, map[string]string{"username": "admin", "password": "correct-horse"}, bootstrapCookie)
	if setupResp.StatusCode != http.StatusOK {
		t.Fatalf("expected credential setup status 200, got %d body=%s", setupResp.StatusCode, readBody(t, setupResp))
	}

	resp = doJSON(t, srv.URL+"/api/status", http.MethodGet, nil, bootstrapCookie)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old bootstrap session invalidated with 401, got %d", resp.StatusCode)
	}

	loginResp = doJSON(t, srv.URL+"/api/login", http.MethodPost, map[string]string{"username": "companion", "password": "companion"}, nil)
	if loginResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected default login disabled after setup, got %d", loginResp.StatusCode)
	}

	loginResp = doJSON(t, srv.URL+"/api/login", http.MethodPost, map[string]string{"username": "admin", "password": "correct-horse"}, nil)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected configured login status 200, got %d body=%s", loginResp.StatusCode, readBody(t, loginResp))
	}
	cookie := sessionFromResponse(t, loginResp)
	resp = doJSON(t, srv.URL+"/api/config", http.MethodGet, nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected authenticated config status 200, got %d", resp.StatusCode)
	}
	var payload map[string]string
	decodeBody(t, resp, &payload)
	if !strings.Contains(payload["config"], "schema_version") {
		t.Fatalf("expected raw config JSON, got %q", payload["config"])
	}
}

func TestLogsRequireConfiguredSession(t *testing.T) {
	configPath, logPath := writeTestFiles(t)
	bootstrapConfiguredAuth(t, configPath, "admin", "correct-horse")
	srv := httptest.NewServer(New(Options{ConfigPath: configPath, LogPath: logPath}).Handler())
	t.Cleanup(srv.Close)

	resp := doJSON(t, srv.URL+"/api/logs", http.MethodGet, nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated logs status 401, got %d", resp.StatusCode)
	}

	loginResp := doJSON(t, srv.URL+"/api/login", http.MethodPost, map[string]string{"username": "admin", "password": "correct-horse"}, nil)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected login status 200, got %d", loginResp.StatusCode)
	}
	resp = doJSON(t, srv.URL+"/api/logs", http.MethodGet, nil, sessionFromResponse(t, loginResp))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected logs status 200, got %d", resp.StatusCode)
	}
	var payload map[string]string
	decodeBody(t, resp, &payload)
	if !strings.Contains(payload["log"], "hello log") {
		t.Fatalf("expected log contents, got %q", payload["log"])
	}
}

func TestLoggingEndpointRequiresConfiguredSessionAndUpdatesLevel(t *testing.T) {
	configPath, logPath := writeTestFiles(t)
	bootstrapConfiguredAuth(t, configPath, "admin", "supersecret")
	originalLevel := logger.GetLevel()
	logger.SetLevel(logger.INFO)
	t.Cleanup(func() { logger.SetLevel(originalLevel) })
	srv := httptest.NewServer(New(Options{ConfigPath: configPath, LogPath: logPath}).Handler())
	t.Cleanup(srv.Close)

	resp := doJSON(t, srv.URL+"/api/logging", http.MethodGet, nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated logging 401, got %d", resp.StatusCode)
	}
	_ = readBody(t, resp)

	loginResp := doJSON(t, srv.URL+"/api/login", http.MethodPost, map[string]string{"username": "admin", "password": "supersecret"}, nil)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d body=%s", loginResp.StatusCode, readBody(t, loginResp))
	}
	cookie := sessionFromResponse(t, loginResp)

	resp = doJSON(t, srv.URL+"/api/logging", http.MethodGet, nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected logging 200, got %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var payload map[string]any
	decodeBody(t, resp, &payload)
	if payload["level"] != "info" || payload["path"] != logPath {
		t.Fatalf("unexpected logging response: %#v", payload)
	}

	resp = doJSON(t, srv.URL+"/api/logging", http.MethodPut, map[string]string{"level": "debug"}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected logging update 200, got %d body=%s", resp.StatusCode, readBody(t, resp))
	}
	if logger.GetLevel() != logger.DEBUG {
		t.Fatalf("expected debug level, got %s", logger.GetLevel().String())
	}
}

func TestCredentialChangeRequiresCurrentPasswordAndInvalidatesSessions(t *testing.T) {
	configPath, logPath := writeTestFiles(t)
	bootstrapConfiguredAuth(t, configPath, "admin", "correct-horse")
	srv := httptest.NewServer(New(Options{ConfigPath: configPath, LogPath: logPath}).Handler())
	t.Cleanup(srv.Close)

	loginResp := doJSON(t, srv.URL+"/api/login", http.MethodPost, map[string]string{"username": "admin", "password": "correct-horse"}, nil)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected login status 200, got %d", loginResp.StatusCode)
	}
	cookie := sessionFromResponse(t, loginResp)

	badResp := doJSON(t, srv.URL+"/api/credentials", http.MethodPost, map[string]string{"username": "owner", "password": "new-password", "current_password": "wrong"}, cookie)
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected wrong current password status 401, got %d", badResp.StatusCode)
	}

	goodResp := doJSON(t, srv.URL+"/api/credentials", http.MethodPost, map[string]string{"username": "owner", "password": "new-password", "current_password": "correct-horse"}, cookie)
	if goodResp.StatusCode != http.StatusOK {
		t.Fatalf("expected credential update status 200, got %d body=%s", goodResp.StatusCode, readBody(t, goodResp))
	}

	resp := doJSON(t, srv.URL+"/api/status", http.MethodGet, nil, cookie)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old configured session invalidated with 401, got %d", resp.StatusCode)
	}

	loginResp = doJSON(t, srv.URL+"/api/login", http.MethodPost, map[string]string{"username": "owner", "password": "new-password"}, nil)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected new login status 200, got %d", loginResp.StatusCode)
	}
}

func TestStatusIncludesUpdateInfo(t *testing.T) {
	configPath, logPath := writeTestFiles(t)
	bootstrapConfiguredAuth(t, configPath, "admin", "correct-horse")

	srv := httptest.NewServer(New(Options{ConfigPath: configPath, LogPath: logPath}).Handler())
	t.Cleanup(srv.Close)

	loginResp := doJSON(t, srv.URL+"/api/login", http.MethodPost, map[string]string{"username": "admin", "password": "correct-horse"}, nil)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected login status 200, got %d", loginResp.StatusCode)
	}

	resp := doJSON(t, srv.URL+"/api/status", http.MethodGet, nil, sessionFromResponse(t, loginResp))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.StatusCode, readBody(t, resp))
	}

	var payload map[string]any
	decodeBody(t, resp, &payload)
	updateStatus, ok := payload["update_status"].(map[string]any)
	if !ok {
		t.Fatalf("expected update_status object, got %#v", payload["update_status"])
	}
	if updateStatus["stage"] != "checking" {
		t.Fatalf("expected update_status.stage=checking, got %v", updateStatus["stage"])
	}
	if _, has := updateStatus["available"]; !has {
		t.Fatalf("expected update_status.available field")
	}
	if _, has := payload["companion_status"]; has {
		t.Fatal("companion_status should not be present")
	}
	if _, has := payload["ha_healthy_proxy"]; has {
		t.Fatal("ha_healthy_proxy should not be present")
	}
}

func writeTestFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	logPath := filepath.Join(dir, "companion.log")
	cfg := config.Default()
	cfg.DeviceModel = "C300X"
	cfg.ClaimCode = "1234-5678"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("hello log\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return configPath, logPath
}

func bootstrapConfiguredAuth(t *testing.T, configPath string, username string, password string) {
	t.Helper()
	srv := New(Options{ConfigPath: configPath})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString(`{"username":"companion","password":"companion"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap login failed: %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/credentials", jsonBody(map[string]string{"username": username, "password": password}))
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range w.Result().Cookies() {
		req.AddCookie(cookie)
	}
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap credentials failed: %d", w.Code)
	}
}

func doJSON(t *testing.T, url string, method string, body any, cookie *http.Cookie) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = jsonBody(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func jsonBody(body any) io.Reader {
	b, _ := json.Marshal(body)
	return bytes.NewReader(b)
}

func sessionFromResponse(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookie {
			return cookie
		}
	}
	t.Fatalf("session cookie not found")
	return nil
}

func decodeBody(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
