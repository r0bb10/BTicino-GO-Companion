package openwebnet

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"bticino-go-companion/internal/config"
)

type fakeAVServer struct {
	ln      net.Listener
	mu      sync.Mutex
	frames  []string
	replies []string
}

func newFakeAVServer(t *testing.T, replies ...string) *fakeAVServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake av listen failed: %v", err)
	}
	s := &fakeAVServer{ln: ln, replies: replies}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeAVServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeAVServer) handle(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 256)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.frames = append(s.frames, string(buf[:n]))
		var reply string
		if len(s.replies) > 0 {
			reply = s.replies[0]
			s.replies = s.replies[1:]
		}
		s.mu.Unlock()
		if reply != "" {
			_, _ = conn.Write([]byte(reply))
		}
	}
}

func (s *fakeAVServer) receivedFrames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.frames...)
}

func newTestAVClient(t *testing.T, s *fakeAVServer, highRes bool) *AVMediaClient {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr failed: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	cfg := config.Default()
	cfg.MediaAVEndpointHost = host
	cfg.MediaAVEndpointPort = port
	cfg.MediaAVHighResVideo = highRes
	c := NewAVMediaClient(cfg)
	c.retryDelay = 10 * time.Millisecond
	c.audioDelay = 10 * time.Millisecond
	c.replyTimeout = 200 * time.Millisecond
	c.dialTimeout = 200 * time.Millisecond
	return c
}

func TestAVMediaClientStreamStartSendsVideoThenAudio(t *testing.T) {
	srv := newFakeAVServer(t, "*#*1##", "*#*1##")
	c := newTestAVClient(t, srv, false)

	if err := c.StreamStart(context.Background(), 5000, 5007); err != nil {
		t.Fatalf("stream start failed: %v", err)
	}
	frames := srv.receivedFrames()
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d: %v", len(frames), frames)
	}
	if frames[0] != "*7*300#127#0#0#1#5007#1*##" {
		t.Fatalf("unexpected video frame: %s", frames[0])
	}
	if frames[1] != "*7*300#127#0#0#1#5000#2*##" {
		t.Fatalf("unexpected audio frame: %s", frames[1])
	}
}

func TestAVMediaClientHighResVideoFrame(t *testing.T) {
	srv := newFakeAVServer(t, "*#*1##", "*#*1##")
	c := newTestAVClient(t, srv, true)

	if err := c.StreamStart(context.Background(), 5000, 5007); err != nil {
		t.Fatalf("stream start failed: %v", err)
	}
	if got := srv.receivedFrames()[0]; got != "*7*300#127#0#0#1#5007#0*##" {
		t.Fatalf("expected high-res video frame, got %s", got)
	}
}

func TestAVMediaClientRetriesVideoOnNAK(t *testing.T) {
	srv := newFakeAVServer(t, "*#*0##", "*#*1##", "*#*1##")
	c := newTestAVClient(t, srv, false)

	if err := c.StreamStart(context.Background(), 5000, 5007); err != nil {
		t.Fatalf("expected retry to recover from NAK: %v", err)
	}
	frames := srv.receivedFrames()
	if len(frames) != 3 || frames[0] != frames[1] {
		t.Fatalf("expected video retry then audio, got %v", frames)
	}
}

func TestAVMediaClientFailsAfterPersistentVideoNAK(t *testing.T) {
	srv := newFakeAVServer(t, "*#*0##", "*#*0##", "*#*0##")
	c := newTestAVClient(t, srv, false)

	err := c.StreamStart(context.Background(), 5000, 5007)
	if err == nil {
		t.Fatal("expected error after persistent NAK")
	}
	if !errors.Is(err, ErrAVCommandRejected) {
		t.Fatalf("expected ErrAVCommandRejected, got: %v", err)
	}
}

func TestAVMediaClientVideoNAKContinuesWhenVideoFlowing(t *testing.T) {
	srv := newFakeAVServer(t, "*#*0##", "*#*0##", "*#*0##", "*#*1##")
	c := newTestAVClient(t, srv, false)
	c.videoConfirmTimeout = 200 * time.Millisecond
	c.videoConfirmPoll = 10 * time.Millisecond
	c.videoConfirmMinDelta = 1
	var count uint64
	c.SetVideoCounter(func() uint64 { return count })
	time.AfterFunc(20*time.Millisecond, func() { count = 2 })

	if err := c.StreamStart(context.Background(), 5000, 5007); err != nil {
		t.Fatalf("video NAK with progressing RTP must not fail stream start: %v", err)
	}
	frames := srv.receivedFrames()
	if len(frames) != 4 {
		t.Fatalf("expected 3 video retries + 1 audio write, got %d", len(frames))
	}
}

func TestAVMediaClientAudioFailureIsBestEffort(t *testing.T) {
	srv := newFakeAVServer(t, "*#*1##", "*#*0##", "*#*0##", "*#*0##")
	c := newTestAVClient(t, srv, false)

	if err := c.StreamStart(context.Background(), 5000, 5007); err != nil {
		t.Fatalf("audio failure must not fail stream start: %v", err)
	}
	if got := len(srv.receivedFrames()); got != 4 {
		t.Fatalf("expected 1 video + 3 audio attempts, got %d", got)
	}
}
