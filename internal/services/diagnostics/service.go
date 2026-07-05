package diagnostics

import (
	"context"
	"sync"
	"time"

	"bticino-go-companion/internal/logger"
	"bticino-go-companion/internal/system"
)

const tag = "services.diagnostics"

const defaultRefreshInterval = 15 * time.Second

type NetworkSnapshot struct {
	IP           string     `json:"ip,omitempty"`
	Netmask      string     `json:"netmask,omitempty"`
	MAC          string     `json:"mac,omitempty"`
	WiFiStrength *int       `json:"wifi_strength,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	Stale        bool       `json:"stale"`
}

type detectorFunc func() (system.NetworkSnapshot, bool)

type Service struct {
	mu       sync.RWMutex
	interval time.Duration
	detect   detectorFunc
	network  NetworkSnapshot
}

func New(interval time.Duration) *Service {
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	svc := &Service{
		interval: interval,
		detect:   system.DetectNetworkSnapshot,
	}
	logger.Debugf(tag, "service created interval=%s", interval)
	return svc
}

func NewForTest(interval time.Duration, detect detectorFunc) *Service {
	svc := New(interval)
	if detect != nil {
		svc.detect = detect
	}
	return svc
}

func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	logger.Debugf(tag, "refresh loop started interval=%s", s.interval)
	defer logger.Debugf(tag, "refresh loop stopped")
	s.Refresh()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Refresh()
		}
	}
}

func (s *Service) Refresh() {
	if s == nil || s.detect == nil {
		return
	}

	netSnap, ok := s.detect()
	if !ok {
		s.mu.Lock()
		wasStale := s.network.Stale
		s.network.Stale = true
		s.mu.Unlock()
		if !wasStale {
			logger.Warnf(tag, "refresh failed stale=true")
		}
		return
	}

	now := time.Now()
	next := NetworkSnapshot{
		IP:        netSnap.IP,
		Netmask:   netSnap.Netmask,
		MAC:       netSnap.MAC,
		UpdatedAt: &now,
		Stale:     false,
	}
	if netSnap.WiFiRSSI != nil {
		v := *netSnap.WiFiRSSI
		next.WiFiStrength = &v
	}

	s.mu.Lock()
	wasStale := s.network.Stale
	previous := s.network
	s.network = next
	s.mu.Unlock()
	if wasStale {
		logger.Infof(tag, "refresh recovered ip=%s mac=%s", next.IP, next.MAC)
	}
	if previous.IP != "" && (previous.IP != next.IP || previous.MAC != next.MAC) {
		logger.Infof(tag, "network changed ip=%s->%s mac=%s->%s", previous.IP, next.IP, previous.MAC, next.MAC)
	} else {
		logger.Debugf(tag, "refresh complete ip=%s mac=%s stale=%t", next.IP, next.MAC, next.Stale)
	}
}

func (s *Service) NetworkSnapshot() NetworkSnapshot {
	if s == nil {
		return NetworkSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.network
	if s.network.WiFiStrength != nil {
		v := *s.network.WiFiStrength
		out.WiFiStrength = &v
	}
	if s.network.UpdatedAt != nil {
		ts := *s.network.UpdatedAt
		out.UpdatedAt = &ts
	}
	return out
}
