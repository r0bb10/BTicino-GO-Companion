package openwebnet

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"bticino-go-companion/internal/config"
	openwebnetproto "bticino-go-companion/internal/protocol/openwebnet"
)

var ErrAVCommandRejected = errors.New("av endpoint rejected command")

// AVMediaClient talks to bt_ipcamera over raw TCP. This is the media routing
// channel used by c300x-controller; SIP starts the AV pipeline, then these
// add-stream commands select the RTP destination consumed by our RTSP ingest.
type AVMediaClient struct {
	addr         string
	streamIP     string
	highRes      bool
	dialTimeout  time.Duration
	replyTimeout time.Duration
	retryDelay   time.Duration
	maxAttempts  int
	audioDelay   time.Duration
	logger       *log.Logger

	mu   sync.Mutex
	conn net.Conn
}

func NewAVMediaClient(cfg config.Config, logger *log.Logger) *AVMediaClient {
	return &AVMediaClient{
		addr:         net.JoinHostPort(strings.TrimSpace(cfg.MediaAVEndpointHost), strconv.Itoa(cfg.MediaAVEndpointPort)),
		streamIP:     "127.0.0.1",
		highRes:      cfg.MediaAVHighResVideo,
		dialTimeout:  5 * time.Second,
		replyTimeout: 5 * time.Second,
		retryDelay:   time.Second,
		maxAttempts:  3,
		audioDelay:   300 * time.Millisecond,
		logger:       logger,
	}
}

func (c *AVMediaClient) StreamStart(ctx context.Context, audioPort, videoPort int) error {
	if audioPort <= 0 || videoPort <= 0 {
		return errors.New("invalid av stream ports")
	}
	video := openwebnetproto.BuildAVAddStreamVideo(c.streamIP, videoPort, c.highRes)
	if err := c.sendCommand(ctx, "add-video-stream", video); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.audioDelay):
	}

	// Audio is best-effort: devices may already start it themselves and NAK a
	// duplicate add. Video is the load-bearing stream for RTSP/WebRTC startup.
	audio := openwebnetproto.BuildAVAddStreamAudio(c.streamIP, audioPort)
	if err := c.sendCommand(ctx, "add-audio-stream", audio); err != nil {
		c.logf("av add-audio-stream failed (continuing with video): %v", err)
	}
	return nil
}

func (c *AVMediaClient) sendCommand(ctx context.Context, label, frame string) error {
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.retryDelay):
			}
		}
		reply, err := c.exchange(frame)
		if err != nil {
			lastErr = err
			c.logf("av %s attempt %d/%d transport error: %v", label, attempt, c.maxAttempts, err)
			continue
		}
		c.logf("av %s attempt %d/%d frame=%q reply=%q", label, attempt, c.maxAttempts, frame, reply)
		if isAllACKs(reply) {
			return nil
		}
		if reply == openwebnetproto.FrameNACK {
			lastErr = fmt.Errorf("%w: NAK", ErrAVCommandRejected)
			continue
		}
		c.closeConn()
		lastErr = fmt.Errorf("%w: unexpected reply %q", ErrAVCommandRejected, reply)
	}
	return fmt.Errorf("av %s failed after %d attempts: %w", label, c.maxAttempts, lastErr)
}

func (c *AVMediaClient) exchange(frame string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		conn, err := net.DialTimeout("tcp", c.addr, c.dialTimeout)
		if err != nil {
			return "", fmt.Errorf("dial %s: %w", c.addr, err)
		}
		c.conn = conn
	}
	if err := c.conn.SetDeadline(time.Now().Add(c.replyTimeout)); err != nil {
		c.closeConnLocked()
		return "", fmt.Errorf("set deadline: %w", err)
	}
	if _, err := c.conn.Write([]byte(frame)); err != nil {
		c.closeConnLocked()
		return "", fmt.Errorf("write frame: %w", err)
	}
	buf := make([]byte, 256)
	n, err := c.conn.Read(buf)
	if err != nil {
		c.closeConnLocked()
		return "", fmt.Errorf("read reply: %w", err)
	}
	return strings.TrimSpace(string(buf[:n])), nil
}

func (c *AVMediaClient) closeConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeConnLocked()
}

func (c *AVMediaClient) closeConnLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *AVMediaClient) logf(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf(format, args...)
	}
}

func isAllACKs(reply string) bool {
	if reply == "" {
		return false
	}
	for reply != "" {
		if !strings.HasPrefix(reply, openwebnetproto.FrameACK) {
			return false
		}
		reply = strings.TrimPrefix(reply, openwebnetproto.FrameACK)
	}
	return true
}
