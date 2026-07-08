package state

import (
	"strings"

	"bticino-go-companion/internal/domain/event"
)

const (
	CallStateIdle    = "idle"
	CallStateRinging = "ringing"
	CallStateActive  = "active"
)

const (
	StreamStateIdle    = "idle"
	StreamStatePreview = "preview"
	StreamStateActive  = "active"
)

func applyTransition(s *Snapshot, ev event.Envelope) {
	kind := strings.TrimSpace(ev.Type)
	entrypointID := strings.TrimSpace(ev.EntrypointID)
	setEntrypoint := entrypointID != ""

	switch kind {
	case event.TypeRingStarted:
		if !setEntrypoint {
			return
		}
		s.Ringing = true
		s.CallState = CallStateRinging
		s.ActiveEntrypoint = entrypointID
	case event.TypeRingEnded:
		s.Ringing = false
		if s.StreamActive {
			s.CallState = CallStateActive
			return
		}
		s.CallState = CallStateIdle
		s.TalkEnabled = false
		s.ActiveEntrypoint = ""
	case event.TypePreviewStarted:
		if !setEntrypoint {
			return
		}
		s.StreamState = StreamStatePreview
		s.StreamActive = false
		s.TalkEnabled = false
		s.ActiveEntrypoint = entrypointID
		if s.CallState == "" || s.CallState == CallStateIdle {
			s.CallState = CallStateRinging
		}
	case event.TypePreviewStopped:
		if s.StreamState == StreamStatePreview {
			s.StreamState = StreamStateIdle
		}
		s.TalkEnabled = false
		if !s.Ringing && !s.StreamActive {
			s.CallState = CallStateIdle
			s.ActiveEntrypoint = ""
		}
	case event.TypeStreamStarted:
		s.StreamActive = true
		s.StreamState = StreamStateActive
		s.CallState = CallStateActive
		if setEntrypoint {
			s.ActiveEntrypoint = entrypointID
		}
	case event.TypeStreamStopped:
		s.StreamActive = false
		s.StreamState = StreamStateIdle
		s.TalkEnabled = false
		if s.Ringing {
			s.CallState = CallStateRinging
			return
		}
		s.CallState = CallStateIdle
		s.ActiveEntrypoint = ""
	case event.TypeCallIncoming:
		if !setEntrypoint {
			return
		}
		s.CallState = CallStateRinging
		s.TalkEnabled = false
		s.ActiveEntrypoint = entrypointID
	case event.TypeCallAnswered:
		s.CallState = CallStateActive
		s.TalkEnabled = true
		if s.StreamState == StreamStatePreview {
			s.StreamState = StreamStateActive
			s.StreamActive = true
		}
	case event.TypeCallEnded:
		if s.StreamActive {
			s.CallState = CallStateActive
			return
		}
		if s.Ringing {
			s.CallState = CallStateRinging
			s.TalkEnabled = false
			return
		}
		s.CallState = CallStateIdle
		s.StreamState = StreamStateIdle
		s.TalkEnabled = false
		s.ActiveEntrypoint = ""
	case event.TypeCallViewRequested:
		if setEntrypoint {
			s.ActiveEntrypoint = entrypointID
		}
	case event.TypeAudioMuted:
		s.AudioMuted = true
	case event.TypeAudioUnmuted:
		s.AudioMuted = false
	case event.TypeVoicemailEnabled:
		s.VoicemailEnabled = true
		if welcomeEnabled, ok := ev.Payload["welcome_message_enabled"].(bool); ok {
			s.VoicemailWelcomeMessageEnabled = welcomeEnabled
		}
	case event.TypeVoicemailDisabled:
		s.VoicemailEnabled = false
		if welcomeEnabled, ok := ev.Payload["welcome_message_enabled"].(bool); ok {
			s.VoicemailWelcomeMessageEnabled = welcomeEnabled
		}
	}
}
