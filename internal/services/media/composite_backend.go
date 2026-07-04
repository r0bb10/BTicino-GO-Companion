package media

import (
	"context"
	"errors"
	"log"
	"sync"
)

const (
	callStateRinging = "ringing"
	callStateActive  = "active"
)

type SIPBackend interface {
	StreamStart(ctx context.Context, devAddr string) error
	StreamStop(ctx context.Context) error
}

type StreamCommandBackend interface {
	StreamStart(ctx context.Context, audioPort, videoPort int) error
}

type CompositeBackendOptions struct {
	SIP       SIPBackend
	Commands  StreamCommandBackend
	AV        StreamCommandBackend
	CallState func() string
	AudioPort int
	VideoPort int
	RequireAV bool
	Logger    *log.Logger
}

type compositeBackend struct {
	opts CompositeBackendOptions

	mu         sync.Mutex
	sipStarted bool
}

func NewCompositeBackend(sip SIPBackend, commands StreamCommandBackend, audioPort, videoPort int) Backend {
	return NewCompositeBackendWithOptions(CompositeBackendOptions{
		SIP:       sip,
		Commands:  commands,
		AudioPort: audioPort,
		VideoPort: videoPort,
	})
}

func NewCompositeBackendWithOptions(opts CompositeBackendOptions) Backend {
	return &compositeBackend{opts: opts}
}

func (b *compositeBackend) StreamStart(ctx context.Context, devAddr string) error {
	if b.opts.AV == nil {
		if b.opts.SIP != nil {
			return b.opts.SIP.StreamStart(ctx, devAddr)
		}
		if b.opts.Commands != nil {
			return b.opts.Commands.StreamStart(ctx, b.opts.AudioPort, b.opts.VideoPort)
		}
		return nil
	}

	sipStarted := false
	var sipErr error
	state := b.callState()
	if state == callStateRinging || state == callStateActive {
		b.logf("media: call state %q; skipping SIP INVITE and adding AV stream only", state)
	} else if b.opts.SIP != nil {
		sipErr = b.opts.SIP.StreamStart(ctx, devAddr)
		switch {
		case sipErr == nil:
			sipStarted = true
		case errors.Is(sipErr, ErrSIPCallInProgress):
			b.logf("media: SIP reported call already in progress; adding AV stream only")
			sipErr = nil
		default:
			return sipErr
		}
	}

	if avErr := b.opts.AV.StreamStart(ctx, b.opts.AudioPort, b.opts.VideoPort); avErr != nil {
		if !b.opts.RequireAV && sipStarted {
			b.logf("media: AV add-stream failed but is not required for this model: %v", avErr)
			b.markSIPStarted(sipStarted)
			return nil
		}
		if sipStarted && b.opts.SIP != nil {
			if stopErr := b.opts.SIP.StreamStop(ctx); stopErr != nil {
				b.logf("media: SIP cleanup after AV stream failure failed: %v", stopErr)
			}
		}
		return errors.Join(sipErr, avErr)
	}

	b.markSIPStarted(sipStarted)
	return nil
}

func (b *compositeBackend) StreamStop(ctx context.Context) error {
	if b.opts.AV == nil {
		if b.opts.SIP != nil {
			return b.opts.SIP.StreamStop(ctx)
		}
		return nil
	}
	b.mu.Lock()
	started := b.sipStarted
	b.sipStarted = false
	b.mu.Unlock()
	if started && b.opts.SIP != nil {
		return b.opts.SIP.StreamStop(ctx)
	}
	return nil
}

func (b *compositeBackend) callState() string {
	if b.opts.CallState == nil {
		return "idle"
	}
	return b.opts.CallState()
}

func (b *compositeBackend) markSIPStarted(started bool) {
	b.mu.Lock()
	b.sipStarted = started
	b.mu.Unlock()
}

func (b *compositeBackend) logf(format string, args ...any) {
	if b.opts.Logger != nil {
		b.opts.Logger.Printf(format, args...)
	}
}
