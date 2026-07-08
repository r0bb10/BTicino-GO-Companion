package state

import (
	"testing"

	"bticino-go-companion/internal/domain/event"
)

func TestApplyTransitionStreamStopKeepsRingingState(t *testing.T) {
	s := Snapshot{
		CallState:    CallStateActive,
		StreamActive: true,
		Ringing:      true,
	}

	applyTransition(&s, event.Envelope{Type: event.TypeStreamStopped})

	if s.StreamActive {
		t.Fatal("expected stream inactive")
	}
	if s.StreamState != StreamStateIdle {
		t.Fatalf("expected idle stream state, got %s", s.StreamState)
	}
	if s.TalkEnabled {
		t.Fatal("expected talk disabled")
	}
	if s.CallState != CallStateRinging {
		t.Fatalf("expected ringing state, got %s", s.CallState)
	}
}

func TestApplyTransitionCallAnsweredSetsActive(t *testing.T) {
	s := Snapshot{CallState: CallStateRinging, StreamState: StreamStatePreview}
	applyTransition(&s, event.Envelope{Type: event.TypeCallAnswered})
	if s.CallState != CallStateActive {
		t.Fatalf("expected active state, got %s", s.CallState)
	}
	if s.StreamState != StreamStateActive || !s.StreamActive {
		t.Fatalf("expected active stream after answering preview, got state=%s active=%v", s.StreamState, s.StreamActive)
	}
	if !s.TalkEnabled {
		t.Fatal("expected talk enabled")
	}
}

func TestApplyTransitionPreviewDoesNotSetActiveCall(t *testing.T) {
	s := Snapshot{CallState: CallStateRinging, Ringing: true}
	applyTransition(&s, event.Envelope{Type: event.TypePreviewStarted, EntrypointID: "gate1"})
	if s.CallState != CallStateRinging {
		t.Fatalf("preview should keep ringing call state, got %s", s.CallState)
	}
	if s.StreamState != StreamStatePreview {
		t.Fatalf("expected preview stream state, got %s", s.StreamState)
	}
	if s.StreamActive {
		t.Fatal("preview should not mark active stream")
	}
	if s.TalkEnabled {
		t.Fatal("preview should not enable talk")
	}
	if s.ActiveEntrypoint != "gate1" {
		t.Fatalf("expected active entrypoint gate1, got %q", s.ActiveEntrypoint)
	}

	applyTransition(&s, event.Envelope{Type: event.TypePreviewStopped})
	if s.StreamState != StreamStateIdle {
		t.Fatalf("expected idle stream state after preview stop, got %s", s.StreamState)
	}
	if s.CallState != CallStateRinging {
		t.Fatalf("preview stop during ring should keep ringing, got %s", s.CallState)
	}
	if s.TalkEnabled {
		t.Fatal("expected talk disabled after preview stop")
	}
}

func TestApplyTransitionIgnoresRingWithoutEntrypoint(t *testing.T) {
	s := Snapshot{CallState: CallStateIdle}
	applyTransition(&s, event.Envelope{Type: event.TypeRingStarted})
	if s.Ringing || s.CallState != CallStateIdle || s.ActiveEntrypoint != "" {
		t.Fatalf("ring without entrypoint should not affect public state: %+v", s)
	}

	applyTransition(&s, event.Envelope{Type: event.TypeCallIncoming})
	if s.Ringing || s.CallState != CallStateIdle || s.ActiveEntrypoint != "" {
		t.Fatalf("incoming call without entrypoint should not affect public state: %+v", s)
	}

	applyTransition(&s, event.Envelope{Type: event.TypePreviewStarted})
	if s.StreamState != "" || s.CallState != CallStateIdle || s.ActiveEntrypoint != "" {
		t.Fatalf("preview without entrypoint should not affect public state: %+v", s)
	}
}

func TestApplyTransitionTracksActiveEntrypoint(t *testing.T) {
	s := Snapshot{}
	applyTransition(&s, event.Envelope{Type: event.TypeCallViewRequested, EntrypointID: "gate2"})
	if s.ActiveEntrypoint != "gate2" {
		t.Fatalf("expected gate2, got %q", s.ActiveEntrypoint)
	}

	applyTransition(&s, event.Envelope{Type: event.TypeStreamStarted, EntrypointID: "gate2"})
	if !s.StreamActive {
		t.Fatal("expected stream active")
	}
	if s.StreamState != StreamStateActive {
		t.Fatalf("expected active stream state, got %s", s.StreamState)
	}
	if s.ActiveEntrypoint != "gate2" {
		t.Fatalf("expected gate2 while streaming, got %q", s.ActiveEntrypoint)
	}

	applyTransition(&s, event.Envelope{Type: event.TypeStreamStopped})
	if s.ActiveEntrypoint != "" {
		t.Fatalf("expected active entrypoint cleared, got %q", s.ActiveEntrypoint)
	}
}

func TestApplyTransitionTracksAudioMute(t *testing.T) {
	s := Snapshot{}
	applyTransition(&s, event.Envelope{Type: event.TypeAudioMuted})
	if !s.AudioMuted {
		t.Fatal("expected audio muted true")
	}
	applyTransition(&s, event.Envelope{Type: event.TypeAudioUnmuted})
	if s.AudioMuted {
		t.Fatal("expected audio muted false")
	}
}

func TestApplyTransitionTracksVoicemailStatus(t *testing.T) {
	s := Snapshot{}
	applyTransition(&s, event.Envelope{
		Type: event.TypeVoicemailEnabled,
		Payload: map[string]any{
			"welcome_message_enabled": true,
		},
	})
	if !s.VoicemailEnabled {
		t.Fatal("expected voicemail enabled true")
	}
	if !s.VoicemailWelcomeMessageEnabled {
		t.Fatal("expected welcome message enabled true")
	}
	applyTransition(&s, event.Envelope{
		Type: event.TypeVoicemailDisabled,
		Payload: map[string]any{
			"welcome_message_enabled": false,
		},
	})
	if s.VoicemailEnabled {
		t.Fatal("expected voicemail enabled false")
	}
	if s.VoicemailWelcomeMessageEnabled {
		t.Fatal("expected welcome message enabled false")
	}
}
