package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigHasEntrypoint(t *testing.T) {
	cfg := Default()
	if !cfg.MDNSEnabled || cfg.MDNSServiceType != "_bticomp._tcp" {
		t.Fatalf("unexpected mDNS defaults: enabled=%v service=%s", cfg.MDNSEnabled, cfg.MDNSServiceType)
	}
	if cfg.OpenWebNetCommandHost != "127.0.0.1" || cfg.OpenWebNetCommandPort != 20000 || cfg.OpenWebNetCommandSec != 3 {
		t.Fatalf("unexpected openwebnet command defaults: host=%s port=%d timeout=%d", cfg.OpenWebNetCommandHost, cfg.OpenWebNetCommandPort, cfg.OpenWebNetCommandSec)
	}
	if cfg.OpenWebNetCommandPassword != "" {
		t.Fatalf("expected empty openwebnet command password by default, got %q", cfg.OpenWebNetCommandPassword)
	}
	if !cfg.MediaRTSPEnabled || cfg.MediaRTSPAddress != ":8554" {
		t.Fatalf("unexpected rtsp defaults: enabled=%v addr=%s", cfg.MediaRTSPEnabled, cfg.MediaRTSPAddress)
	}
	if cfg.MediaRTPAudioPort != 5000 || cfg.MediaRTPVideoPort != 5007 {
		t.Fatalf("unexpected rtp defaults: audio=%d video=%d", cfg.MediaRTPAudioPort, cfg.MediaRTPVideoPort)
	}
	if cfg.MediaAVEndpointHost != "127.0.0.1" || cfg.MediaAVEndpointPort != 30007 || !cfg.MediaAVHighResVideo {
		t.Fatalf("unexpected av endpoint defaults: host=%s port=%d highres=%v", cfg.MediaAVEndpointHost, cfg.MediaAVEndpointPort, cfg.MediaAVHighResVideo)
	}
	if len(cfg.Entrypoints) != 1 {
		t.Fatalf("expected 1 default entrypoint, got %d", len(cfg.Entrypoints))
	}
	ep := cfg.Entrypoints[0]
	if ep.DevAddr != "20" || !ep.HasStream || !ep.HasUnlock || !ep.HasRing {
		t.Fatalf("unexpected default entrypoint: %+v", ep)
	}
}

func TestSaveLoadPersistsOpenWebNetCommandPassword(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	cfg.OpenWebNetCommandPassword = "pw123"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.OpenWebNetCommandPassword != "pw123" {
		t.Fatalf("expected persisted openwebnet command password, got %q", loaded.OpenWebNetCommandPassword)
	}
}

func TestSaveOmitsEmptyWebAuth(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(raw), "web_auth") {
		t.Fatalf("expected empty web_auth to be omitted, got: %s", string(raw))
	}
}

func TestSaveLoadPersistsWebAuth(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	cfg.WebAuth = WebAuthConfig{Enabled: true, Username: "admin", PasswordHash: "hash", SessionSecret: "secret"}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !loaded.WebAuth.Enabled || loaded.WebAuth.Username != "admin" || loaded.WebAuth.PasswordHash != "hash" || loaded.WebAuth.SessionSecret != "secret" {
		t.Fatalf("unexpected web auth after load: %+v", loaded.WebAuth)
	}
}

func TestSaveLoadPersistsWebUIConfig(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	cfg.WebUI.ListenAddr = ":443"
	cfg.WebUI.TLS.Enabled = true
	cfg.WebUI.TLS.CertFile = "/cfg/web.crt"
	cfg.WebUI.TLS.KeyFile = "/cfg/web.key"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.WebUI.ListenAddr != ":443" || !loaded.WebUI.TLS.Enabled || loaded.WebUI.TLS.CertFile != "/cfg/web.crt" || loaded.WebUI.TLS.KeyFile != "/cfg/web.key" {
		t.Fatalf("unexpected web ui config after load: %+v", loaded.WebUI)
	}
}

func TestSaveLoadPersistsDeviceModel(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	cfg.DeviceModel = "C100X"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.DeviceModel != "C100X" {
		t.Fatalf("expected persisted model C100X, got %q", loaded.DeviceModel)
	}
}

func TestC100XUsesLowResAVDefault(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	cfg.DeviceModel = "C100X"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.MediaAVHighResVideo {
		t.Fatal("expected C100X to default to low-res AV video")
	}
	if loaded.MediaAVEndpointHost != "127.0.0.1" || loaded.MediaAVEndpointPort != 30007 {
		t.Fatalf("unexpected C100X av endpoint: %s:%d", loaded.MediaAVEndpointHost, loaded.MediaAVEndpointPort)
	}
}

func TestAVAddStreamRequirementByModel(t *testing.T) {
	if RequireAVAddStream("C300X") {
		t.Fatal("expected C300X AV add-stream to be optional after SIP success")
	}
	if !RequireAVAddStream("C100X") {
		t.Fatal("expected C100X AV add-stream to be required")
	}
}

func TestResolveDefaultStreamDevAddr(t *testing.T) {
	if got := ResolveDefaultStreamDevAddr("C300X", "20"); got != "20" {
		t.Fatalf("expected C300X stream devaddr fallback 20, got %q", got)
	}
	if got := ResolveDefaultStreamDevAddr("C100X", "20"); got != "20" {
		t.Fatalf("expected C100X stream devaddr fallback 20 when modules file is absent, got %q", got)
	}
	if got := ResolveDefaultStreamDevAddr("", ""); got != "20" {
		t.Fatalf("expected empty stream devaddr fallback 20, got %q", got)
	}
}

func TestDetectC100XStreamDevAddr(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "mymodules")
	originalPath := c100xModulesPath
	c100xModulesPath = path
	t.Cleanup(func() { c100xModulesPath = originalPath })

	body := `{
  "modules": [
    {"id": "12", "system": "videodoorentry", "deviceType": "EU", "privateAddress": {"addressValues": [{"value": "20"}] }},
    {"id": "34", "system": "lighting", "deviceType": "EU", "privateAddress": {"addressValues": [{"value": "20"}] }}
  ]
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write modules file: %v", err)
	}

	if got := detectC100XStreamDevAddr(); got != "12" {
		t.Fatalf("expected detected C100X stream devaddr 12, got %q", got)
	}
	if got := ResolveDefaultStreamDevAddr("C100X", "20"); got != "12" {
		t.Fatalf("expected C100X stream devaddr 12, got %q", got)
	}
}

func TestSaveLoadDoesNotPersistRuntimeDiagnosticMetadata(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	cfg.DeviceFirmware = "9.8.7"
	cfg.DeviceHardware = "3.2.1"
	cfg.DeviceKernel = "6.1.2"
	cfg.DeviceDistribution = "1.2.3"
	cfg.DeviceIP = "192.0.2.172"
	cfg.DeviceNetmask = "255.255.255.0"
	cfg.DeviceMAC = "00:11:22:33:44:55"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	// Runtime diagnostics are not part of persisted schema config and
	// should not be reloaded from disk.
	if loaded.DeviceFirmware != "unknown" || loaded.DeviceHardware != "unknown" || loaded.DeviceKernel != "unknown" || loaded.DeviceDistribution != "unknown" || loaded.DeviceIP != "" || loaded.DeviceNetmask != "" || loaded.DeviceMAC != "" {
		t.Fatalf("runtime diagnostics should not persist in config.json, got fw=%q hw=%q kernel=%q dist=%q ip=%q netmask=%q mac=%q", loaded.DeviceFirmware, loaded.DeviceHardware, loaded.DeviceKernel, loaded.DeviceDistribution, loaded.DeviceIP, loaded.DeviceNetmask, loaded.DeviceMAC)
	}
}

func TestSaveUsesNestedSystemAndConfigSchema(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	cfg.ExposeMuteControl = true
	cfg.SystemRebootEnabled = true
	cfg.SystemServices = map[string]SystemServiceConfig{"dropbear": {Enabled: true, Exposed: true}}
	cfg.ExposeVoicemailToggle = true
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "\"system\"") {
		t.Fatalf("expected nested system schema, got: %s", text)
	}
	if !strings.Contains(text, "\"companion\"") {
		t.Fatalf("expected nested companion schema, got: %s", text)
	}
	if !strings.Contains(text, "\"schema_version\": 2") {
		t.Fatalf("expected schema version 2, got: %s", text)
	}
	if !strings.Contains(text, "\"update\"") {
		t.Fatalf("expected system.control.update block, got: %s", text)
	}
}

func TestSaveLoadPersistsSystemUpdateControl(t *testing.T) {
	tDir := t.TempDir()
	path := filepath.Join(tDir, "config.json")

	cfg := Default()
	cfg.SystemUpdateEnabled = true
	cfg.SystemUpdateExposed = true
	cfg.SystemUpdateAllowRollback = true
	cfg.UpdateReleaseRepo = "owner/repo"
	cfg.UpdateReleaseAsset = "companion.tar.gz"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !loaded.SystemUpdateEnabled || !loaded.SystemUpdateExposed || !loaded.SystemUpdateAllowRollback {
		t.Fatalf("expected persisted update controls, got enabled=%v exposed=%v rollback=%v", loaded.SystemUpdateEnabled, loaded.SystemUpdateExposed, loaded.SystemUpdateAllowRollback)
	}
	if loaded.UpdateReleaseRepo != "owner/repo" {
		t.Fatalf("expected persisted release repo owner/repo, got %q", loaded.UpdateReleaseRepo)
	}
}
