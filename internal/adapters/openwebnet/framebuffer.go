package openwebnet

import (
	"sync"
	"time"

	"bticino-go-companion/internal/domain/event"
	openwebnetproto "bticino-go-companion/internal/protocol/openwebnet"
)

type FrameEntry struct {
	Time   time.Time `json:"t"`
	System string    `json:"sys"`
	Raw    string    `json:"raw"`
	Mapped bool      `json:"mapped"`
	Events int       `json:"events"`
}

type FrameBuffer struct {
	mu     sync.Mutex
	pos    int
	full   bool
	frames []FrameEntry
}

func NewFrameBuffer(capacity int) *FrameBuffer {
	if capacity <= 0 {
		capacity = 200
	}
	return &FrameBuffer{
		frames: make([]FrameEntry, capacity),
	}
}

func (b *FrameBuffer) Push(msg openwebnetproto.Message, mapped []event.Envelope) {
	entry := FrameEntry{
		Time:   time.Now(),
		System: msg.System,
		Raw:    msg.Raw,
		Mapped: len(mapped) > 0,
		Events: len(mapped),
	}
	b.mu.Lock()
	b.frames[b.pos] = entry
	b.pos++
	if b.pos == len(b.frames) {
		b.pos = 0
		b.full = true
	}
	b.mu.Unlock()
}

func (b *FrameBuffer) Snapshot() []FrameEntry {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := len(b.frames)
	if !b.full {
		n = b.pos
	}
	out := make([]FrameEntry, n)
	if b.full {
		copy(out, b.frames[b.pos:])
		copy(out[len(b.frames)-b.pos:], b.frames[:b.pos])
	} else {
		copy(out, b.frames[:b.pos])
	}
	return out
}
