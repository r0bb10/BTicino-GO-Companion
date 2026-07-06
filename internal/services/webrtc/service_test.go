package webrtc

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"bticino-go-companion/internal/domain/entrypoint"

	"github.com/pion/webrtc/v4"
)

type lifecycleStub struct{}

func (l lifecycleStub) ReaderJoin(context.Context, string, string, string) error { return nil }
func (l lifecycleStub) ReaderLeave(context.Context, string) error                { return nil }

type blockingLifecycle struct {
	started chan struct{}
	release chan struct{}
}

func (l *blockingLifecycle) ReaderJoin(ctx context.Context, _ string, _ string, _ string) error {
	close(l.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.release:
		return nil
	}
}

func (l *blockingLifecycle) ReaderLeave(context.Context, string) error { return nil }

func newServiceForTest(t *testing.T) *Service {
	t.Helper()
	return newServiceForTestWithStream(t, nil, nil)
}

func newServiceForTestWithStream(t *testing.T, stream StreamLifecycle, entrypoints []entrypoint.Model) *Service {
	t.Helper()
	origPort := webrtcICEPort
	origPreferred := preferredOutboundInterface
	webrtcICEPort = 0
	preferredOutboundInterface = func() (net.Interface, net.IP, error) {
		return net.Interface{Name: "lo"}, net.ParseIP("127.0.0.1"), nil
	}
	t.Cleanup(func() {
		webrtcICEPort = origPort
		preferredOutboundInterface = origPreferred
	})
	svc, err := New(stream, nil, entrypoints, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func TestHandleOfferAdvertisesMonoOpusFMTP(t *testing.T) {
	svc := newServiceForTestWithStream(t, lifecycleStub{}, []entrypoint.Model{{ID: "main", DevAddr: "20", HasStream: true}})
	offer := newBrowserLikeOffer(t)

	result, err := svc.HandleOffer(context.Background(), "session-1", "main", offer)
	if err != nil {
		t.Fatalf("handle offer: %v", err)
	}

	if !strings.Contains(result.AnswerSDP, "a=rtpmap:111 opus/48000/2") {
		t.Fatalf("expected browser-compatible opus rtpmap in answer SDP:\n%s", result.AnswerSDP)
	}
	if !strings.Contains(result.AnswerSDP, "a=fmtp:111") || !strings.Contains(result.AnswerSDP, "stereo=0") || !strings.Contains(result.AnswerSDP, "sprop-stereo=0") {
		t.Fatalf("expected mono opus fmtp in answer SDP:\n%s", result.AnswerSDP)
	}
}

func TestHandleOfferReturnsAnswerBeforeReaderJoinCompletes(t *testing.T) {
	lifecycle := &blockingLifecycle{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := newServiceForTestWithStream(t, lifecycle, []entrypoint.Model{{ID: "main", DevAddr: "20", HasStream: true}})
	offer := newBrowserLikeOffer(t)
	done := make(chan struct {
		result OfferResult
		err    error
	}, 1)

	go func() {
		result, err := svc.HandleOffer(context.Background(), "session-1", "main", offer)
		done <- struct {
			result OfferResult
			err    error
		}{result: result, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("handle offer: %v", got.err)
		}
		if strings.TrimSpace(got.result.AnswerSDP) == "" {
			t.Fatal("expected answer SDP")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("HandleOffer waited for ReaderJoin instead of returning the SDP answer")
	}

	select {
	case <-lifecycle.started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected async ReaderJoin to start")
	}
	close(lifecycle.release)
}

func newBrowserLikeOffer(t *testing.T) string {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("new peer connection: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatalf("add video transceiver: %v", err)
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatalf("add audio transceiver: %v", err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("set local description: %v", err)
	}
	<-gatherDone
	local := pc.LocalDescription()
	if local == nil || strings.TrimSpace(local.SDP) == "" {
		t.Fatal("empty local offer")
	}
	return local.SDP
}

func TestAddCandidateQueuesUnknownSession(t *testing.T) {
	svc := newServiceForTest(t)

	candidate := Candidate{
		Candidate: "candidate:1 1 UDP 2122260223 10.0.0.5 54321 typ host",
	}
	if err := svc.AddCandidate("session-1", candidate); err != nil {
		t.Fatalf("add candidate: %v", err)
	}

	svc.mu.RLock()
	batch, ok := svc.pendingCandidates["session-1"]
	svc.mu.RUnlock()
	if !ok {
		t.Fatalf("expected pending candidates for session")
	}
	if len(batch.candidates) != 1 {
		t.Fatalf("expected 1 pending candidate, got %d", len(batch.candidates))
	}
	if batch.candidates[0].Candidate != candidate.Candidate {
		t.Fatalf("unexpected candidate content: %s", batch.candidates[0].Candidate)
	}
}

func TestAddCandidateRejectsEmptyCandidate(t *testing.T) {
	svc := newServiceForTest(t)
	if err := svc.AddCandidate("session-1", Candidate{}); err != ErrCandidateRequired {
		t.Fatalf("expected ErrCandidateRequired, got %v", err)
	}
}

func TestCloseUnknownSessionClearsPendingCandidates(t *testing.T) {
	svc := newServiceForTest(t)
	_ = svc.AddCandidate("session-1", Candidate{Candidate: "candidate:1 1 UDP 2122260223 10.0.0.5 54321 typ host"})

	if err := svc.Close("session-1"); err != nil {
		t.Fatalf("close: %v", err)
	}

	svc.mu.RLock()
	_, ok := svc.pendingCandidates["session-1"]
	svc.mu.RUnlock()
	if ok {
		t.Fatalf("expected pending candidates to be cleared on close")
	}
}

func TestPendingCandidatePrune(t *testing.T) {
	svc := newServiceForTest(t)

	svc.mu.Lock()
	svc.pendingCandidates["expired"] = pendingCandidateBatch{
		candidates: []webrtc.ICECandidateInit{{Candidate: "candidate:expired 1 UDP 1 10.0.0.1 1 typ host"}},
		updatedAt:  time.Now().Add(-(pendingCandidateTTL + time.Second)),
	}
	svc.mu.Unlock()

	_ = svc.AddCandidate("fresh", Candidate{Candidate: "candidate:1 1 UDP 2122260223 10.0.0.5 54321 typ host"})

	svc.mu.RLock()
	_, expiredExists := svc.pendingCandidates["expired"]
	_, freshExists := svc.pendingCandidates["fresh"]
	svc.mu.RUnlock()
	if expiredExists {
		t.Fatalf("expected expired pending candidates to be pruned")
	}
	if !freshExists {
		t.Fatalf("expected fresh pending candidates to remain")
	}
}

func TestCanonicalizeSDP(t *testing.T) {
	raw := "v=0\n" +
		"o=- 1 2 IN IP4 127.0.0.1\n" +
		"s=-\n" +
		"t=0 0\n"

	got := canonicalizeSDP(raw)
	if got == "" {
		t.Fatalf("expected canonicalized sdp")
	}
	if got[len(got)-2:] != "\r\n" {
		t.Fatalf("expected trailing CRLF, got %q", got)
	}
	if !strings.Contains(got, "\r\no=- 1 2 IN IP4 127.0.0.1\r\n") {
		t.Fatalf("expected normalized CRLF lines, got %q", got)
	}
}

func TestAnswerDirectionFromOfferAudio(t *testing.T) {
	tests := []struct {
		name    string
		offer   string
		wantDir webrtc.RTPTransceiverDirection
	}{
		{
			name: "audio recvonly maps to sendonly",
			offer: "v=0\r\n" +
				"o=- 1 1 IN IP4 127.0.0.1\r\n" +
				"s=-\r\n" +
				"t=0 0\r\n" +
				"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
				"a=recvonly\r\n",
			wantDir: webrtc.RTPTransceiverDirectionSendonly,
		},
		{
			name: "audio sendrecv maps to sendrecv",
			offer: "v=0\r\n" +
				"o=- 1 1 IN IP4 127.0.0.1\r\n" +
				"s=-\r\n" +
				"t=0 0\r\n" +
				"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
				"a=sendrecv\r\n",
			wantDir: webrtc.RTPTransceiverDirectionSendrecv,
		},
		{
			name: "audio sendonly maps to recvonly",
			offer: "v=0\r\n" +
				"o=- 1 1 IN IP4 127.0.0.1\r\n" +
				"s=-\r\n" +
				"t=0 0\r\n" +
				"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
				"a=sendonly\r\n",
			wantDir: webrtc.RTPTransceiverDirectionRecvonly,
		},
		{
			name: "audio inherits session direction",
			offer: "v=0\r\n" +
				"o=- 1 1 IN IP4 127.0.0.1\r\n" +
				"s=-\r\n" +
				"t=0 0\r\n" +
				"a=recvonly\r\n" +
				"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n",
			wantDir: webrtc.RTPTransceiverDirectionSendonly,
		},
		{
			name: "no audio mline maps inactive",
			offer: "v=0\r\n" +
				"o=- 1 1 IN IP4 127.0.0.1\r\n" +
				"s=-\r\n" +
				"t=0 0\r\n" +
				"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
				"a=recvonly\r\n",
			wantDir: webrtc.RTPTransceiverDirectionInactive,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := answerDirectionFromOfferAudio(tc.offer)
			if got != tc.wantDir {
				t.Fatalf("answerDirectionFromOfferAudio()=%s want=%s", got.String(), tc.wantDir.String())
			}
		})
	}
}
