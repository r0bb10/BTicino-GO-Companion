package media

import (
	"context"
	"errors"
	"testing"
)

type sipStub struct {
	startCalls int
	stopCalls  int
	devAddr    string
	startErr   error
	stopErr    error
}

func (s *sipStub) StreamStart(_ context.Context, devAddr string) error {
	s.startCalls++
	s.devAddr = devAddr
	return s.startErr
}

func (s *sipStub) StreamStop(_ context.Context) error {
	s.stopCalls++
	return s.stopErr
}

type commandStub struct {
	startCalls int
	audioPort  int
	videoPort  int
	startErr   error
}

func (c *commandStub) StreamStart(_ context.Context, audioPort, videoPort int) error {
	c.startCalls++
	c.audioPort = audioPort
	c.videoPort = videoPort
	return c.startErr
}

func TestCompositeBackendLegacyStreamStartUsesSIPWhenAvailable(t *testing.T) {
	sip := &sipStub{}
	cmd := &commandStub{}
	backend := NewCompositeBackend(sip, cmd, 5000, 5007)

	if err := backend.StreamStart(context.Background(), "20"); err != nil {
		t.Fatalf("stream start failed: %v", err)
	}
	if sip.startCalls != 1 || sip.devAddr != "20" {
		t.Fatalf("unexpected sip calls=%d devaddr=%q", sip.startCalls, sip.devAddr)
	}
	if cmd.startCalls != 0 {
		t.Fatalf("expected no command stream start when SIP is available, got %d", cmd.startCalls)
	}
}

func TestCompositeBackendStreamStartRunsSIPThenAV(t *testing.T) {
	sip := &sipStub{}
	av := &commandStub{}
	backend := NewCompositeBackendWithOptions(CompositeBackendOptions{
		SIP:       sip,
		AV:        av,
		AudioPort: 5000,
		VideoPort: 5007,
	})

	if err := backend.StreamStart(context.Background(), "20"); err != nil {
		t.Fatalf("stream start failed: %v", err)
	}
	if sip.startCalls != 1 || sip.devAddr != "20" {
		t.Fatalf("unexpected sip calls=%d devaddr=%q", sip.startCalls, sip.devAddr)
	}
	if av.startCalls != 1 || av.audioPort != 5000 || av.videoPort != 5007 {
		t.Fatalf("unexpected av calls=%d audio=%d video=%d", av.startCalls, av.audioPort, av.videoPort)
	}
}

func TestCompositeBackendStreamStartSkipsSIPWhenCallActive(t *testing.T) {
	sip := &sipStub{}
	av := &commandStub{}
	backend := NewCompositeBackendWithOptions(CompositeBackendOptions{
		SIP:       sip,
		AV:        av,
		CallState: func() string { return "active" },
		AudioPort: 5000,
		VideoPort: 5007,
	})

	if err := backend.StreamStart(context.Background(), "20"); err != nil {
		t.Fatalf("stream start failed: %v", err)
	}
	if sip.startCalls != 0 {
		t.Fatalf("expected SIP to be skipped for active call, got %d", sip.startCalls)
	}
	if av.startCalls != 1 {
		t.Fatalf("expected AV start, got %d", av.startCalls)
	}
}

func TestCompositeBackend486FallsThroughToAV(t *testing.T) {
	sip := &sipStub{startErr: ErrSIPCallInProgress}
	av := &commandStub{}
	backend := NewCompositeBackendWithOptions(CompositeBackendOptions{
		SIP:       sip,
		AV:        av,
		AudioPort: 5000,
		VideoPort: 5007,
	})

	if err := backend.StreamStart(context.Background(), "20"); err != nil {
		t.Fatalf("stream start failed: %v", err)
	}
	if sip.startCalls != 1 || av.startCalls != 1 {
		t.Fatalf("expected SIP then AV, got sip=%d av=%d", sip.startCalls, av.startCalls)
	}
}

func TestCompositeBackendAVFailureAfterSIPSuccessCleansUp(t *testing.T) {
	sip := &sipStub{}
	av := &commandStub{startErr: errors.New("av failed")}
	backend := NewCompositeBackendWithOptions(CompositeBackendOptions{
		SIP:       sip,
		AV:        av,
		AudioPort: 5000,
		VideoPort: 5007,
		RequireAV: true,
	})

	if err := backend.StreamStart(context.Background(), "20"); err == nil {
		t.Fatal("expected stream start error")
	}
	if sip.stopCalls != 1 {
		t.Fatalf("expected SIP cleanup, got %d", sip.stopCalls)
	}
}

func TestCompositeBackendOptionalAVFailureAfterSIPSuccessStillStarts(t *testing.T) {
	sip := &sipStub{}
	av := &commandStub{startErr: errors.New("av failed")}
	backend := NewCompositeBackendWithOptions(CompositeBackendOptions{
		SIP:       sip,
		AV:        av,
		AudioPort: 5000,
		VideoPort: 5007,
	})

	if err := backend.StreamStart(context.Background(), "20"); err != nil {
		t.Fatalf("optional AV failure after SIP success must not fail stream start: %v", err)
	}
	if sip.stopCalls != 0 {
		t.Fatalf("did not expect SIP cleanup for optional AV failure, got %d", sip.stopCalls)
	}
}

func TestCompositeBackendStreamStartUsesCommandsWhenSIPMissing(t *testing.T) {
	cmd := &commandStub{startErr: errors.New("command failed")}
	backend := NewCompositeBackend(nil, cmd, 5000, 5007)

	err := backend.StreamStart(context.Background(), "20")
	if err == nil {
		t.Fatal("expected stream start error")
	}
	if cmd.startCalls != 1 {
		t.Fatalf("expected command stream start attempt, got %d", cmd.startCalls)
	}
}

func TestCompositeBackendStreamStopCallsSIP(t *testing.T) {
	sip := &sipStub{}
	backend := NewCompositeBackend(sip, nil, 5000, 5007)

	if err := backend.StreamStop(context.Background()); err != nil {
		t.Fatalf("stream stop failed: %v", err)
	}
	if sip.stopCalls != 1 {
		t.Fatalf("expected sip stop call, got %d", sip.stopCalls)
	}
}
