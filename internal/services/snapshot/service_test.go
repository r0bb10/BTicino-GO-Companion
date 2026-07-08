package snapshot

import (
	"testing"

	"bticino-go-companion/internal/services/media"
	"bticino-go-companion/internal/services/state"
)

type stateStub struct {
	snap state.Snapshot
}

func (s stateStub) Snapshot() state.Snapshot {
	return s.snap
}

func TestSelectCaptureSourceUsesExistingPreviewForSameEntrypoint(t *testing.T) {
	svc := &Service{state: stateStub{snap: state.Snapshot{StreamState: state.StreamStatePreview, ActiveEntrypoint: "gate3"}}}
	selection := svc.selectCaptureSource("gate3", media.Snapshot{})
	if selection.Blocked || !selection.UseExisting {
		t.Fatalf("expected existing preview media, got %+v", selection)
	}
	if selection.Mode != state.StreamStatePreview {
		t.Fatalf("expected preview mode, got %q", selection.Mode)
	}
}

func TestSelectCaptureSourceBlocksPreviewForDifferentEntrypoint(t *testing.T) {
	svc := &Service{state: stateStub{snap: state.Snapshot{StreamState: state.StreamStatePreview, ActiveEntrypoint: "gate3"}}}
	selection := svc.selectCaptureSource("gate2", media.Snapshot{})
	if !selection.Blocked {
		t.Fatalf("expected capture blocked, got %+v", selection)
	}
}

func TestSelectCaptureSourceUsesExistingActiveMediaForSameEntrypoint(t *testing.T) {
	svc := &Service{}
	selection := svc.selectCaptureSource("gate2", media.Snapshot{StreamActive: true, ActiveEntrypoint: "gate2"})
	if selection.Blocked || !selection.UseExisting {
		t.Fatalf("expected existing active media, got %+v", selection)
	}
}

func TestSelectCaptureSourceBlocksActiveMediaForDifferentEntrypoint(t *testing.T) {
	svc := &Service{}
	selection := svc.selectCaptureSource("gate2", media.Snapshot{StreamActive: true, ActiveEntrypoint: "gate3"})
	if !selection.Blocked {
		t.Fatalf("expected capture blocked, got %+v", selection)
	}
}

func TestSelectCaptureSourceStartsStreamWhenIdle(t *testing.T) {
	svc := &Service{state: stateStub{snap: state.Snapshot{StreamState: state.StreamStateIdle, ActiveEntrypoint: "none"}}}
	selection := svc.selectCaptureSource("gate2", media.Snapshot{})
	if selection.Blocked || selection.UseExisting {
		t.Fatalf("expected idle capture to start stream, got %+v", selection)
	}
}
