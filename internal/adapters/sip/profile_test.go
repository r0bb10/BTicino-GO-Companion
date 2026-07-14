package sipadapter

import (
	"os"
	"path/filepath"
	"testing"

	"bticino-go-companion/internal/config"
)

func TestResolveSIPConfigUsesConfiguredIdentity(t *testing.T) {
	dir := t.TempDir()

	domainPath := filepath.Join(dir, "domain-registration.conf")
	if err := os.WriteFile(domainPath, []byte("example.local transport=tcp\n"), 0o644); err != nil {
		t.Fatalf("write domain file: %v", err)
	}

	originalDomainPaths := flexisipDomainRegistrationPaths
	originalConfigPaths := flexisipConfigPaths
	flexisipDomainRegistrationPaths = []string{domainPath}
	flexisipConfigPaths = []string{}
	t.Cleanup(func() {
		flexisipDomainRegistrationPaths = originalDomainPaths
		flexisipConfigPaths = originalConfigPaths
	})

	cfg := config.Default()
	cfg.MediaSIPFrom = "companion@127.0.0.1"
	cfg.MediaSIPDomain = ""
	cfg.MediaSIPAuthUser = ""

	resolved, err := resolveSIPConfig(cfg)
	if err != nil {
		t.Fatalf("resolveSIPConfig failed: %v", err)
	}
	if resolved.MediaSIPDomain != "example.local" {
		t.Fatalf("unexpected domain: %s", resolved.MediaSIPDomain)
	}
	if resolved.MediaSIPFrom != "companion@127.0.0.1" {
		t.Fatalf("unexpected from: %s", resolved.MediaSIPFrom)
	}
	if resolved.MediaSIPAuthUser != "companion" {
		t.Fatalf("unexpected auth user: %s", resolved.MediaSIPAuthUser)
	}
}

func TestResolveSIPConfigUsesCompanionIdentity(t *testing.T) {
	cfg := config.Default()
	cfg.MediaSIPFrom = "webrtc@127.0.0.1"
	cfg.MediaSIPAuthUser = "webrtc"

	resolved, err := resolveSIPConfig(cfg)
	if err != nil {
		t.Fatalf("resolveSIPConfig failed: %v", err)
	}
	if resolved.MediaSIPFrom != "companion@127.0.0.1" {
		t.Fatalf("unexpected from: %s", resolved.MediaSIPFrom)
	}
	if resolved.MediaSIPAuthUser != "companion" {
		t.Fatalf("unexpected auth user: %s", resolved.MediaSIPAuthUser)
	}
}

func TestResolveSIPConfigUsesModelTarget(t *testing.T) {
	originalDomainPaths := flexisipDomainRegistrationPaths
	originalConfigPaths := flexisipConfigPaths
	flexisipDomainRegistrationPaths = []string{}
	flexisipConfigPaths = []string{}
	t.Cleanup(func() {
		flexisipDomainRegistrationPaths = originalDomainPaths
		flexisipConfigPaths = originalConfigPaths
	})

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "c300x", model: "C300X", want: "c300x@127.0.0.1"},
		{name: "c100x", model: "C100X", want: "c100x@127.0.0.1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.DeviceModel = tc.model
			cfg.MediaSIPTo = ""

			resolved, err := resolveSIPConfig(cfg)
			if err != nil {
				t.Fatalf("resolveSIPConfig failed: %v", err)
			}
			if resolved.MediaSIPTo != tc.want {
				t.Fatalf("expected target %q, got %q", tc.want, resolved.MediaSIPTo)
			}
		})
	}
}
