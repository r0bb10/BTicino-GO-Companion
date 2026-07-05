package openwebnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"bticino-go-companion/internal/config"
	"bticino-go-companion/internal/logger"
	openwebnetproto "bticino-go-companion/internal/protocol/openwebnet"
)

const tag = "adapters.openwebnet.av"

var ErrAVCommandRejected = errors.New("av endpoint rejected command")

type AVMediaClient struct {
	addr         string
	streamIP     string
	highRes      bool
	dialTimeout  time.Duration
	replyTimeout time.Duration
	retryDelay   time.Duration
	maxAttempts  int
	audioDelay   time.Duration

	mu   sync.Mutex
	conn net.Conn
}

func NewAVMediaClient(cfg config.Config) *AVMediaClient {
	return &AVMediaClient{
		addr:         net.JoinHostPort(strings.TrimSpace(cfg.MediaAVEndpointHost), strconv.Itoa(cfg.MediaAVEndpointPort)),
		streamIP:     "127.0.0.1",
		highRes:      cfg.MediaAVHighResVideo,
		dialTimeout:  5 * time.Second,
		replyTimeout: 5 * time.Second,
		retryDelay:   time.Second,
		maxAttempts:  3,
		audioDelay:   300 * time.Millisecond,
	}
}

func (c *AVMediaClient) StreamStart(ctx context.Context, audioPort, videoPort int) error {
	if audioPort <= 0 || videoPort <= 0 {
		logger.Warnf(tag, "stream start rejected reason=invalid_ports audio=%d video=%d", audioPort, videoPort)
		return errors.New("invalid av stream ports")
	}
	video := openwebnetproto.BuildAVAddStreamVideo(c.streamIP, videoPort, c.highRes)
	if err := c.sendCommand(ctx, "add-video-stream", video); err != nil {
		logger.Errorf(tag, "add-video-stream failed audio_port=%d video_port=%d err=%v", audioPort, videoPort, err)
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.audioDelay):
	}

	audio := openwebnetproto.BuildAVAddStreamAudio(c.streamIP, audioPort)
	if err := c.sendCommand(ctx, "add-audio-stream", audio); err != nil {
		logger.Warnf(tag, "add-audio-stream failed audio_port=%d video_port=%d err=%v", audioPort, videoPort, err)
	}
	logger.Infof(tag, "stream start complete audio_port=%d video_port=%d highres=%t", audioPort, videoPort, c.highRes)
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
			logger.Debugf(tag, "command attempt failed label=%s attempt=%d/%d err=%v", label, attempt, c.maxAttempts, err)
			continue
		}
		logger.Debugf(tag, "command reply label=%s attempt=%d/%d frame=%q reply=%q", label, attempt, c.maxAttempts, frame, reply)
		if isAllACKs(reply) {
			return nil
		}
		if reply == openwebnetproto.FrameNACK {
			lastErr = fmt.Errorf("%w: NAK", ErrAVCommandRejected)
			logger.Debugf(tag, "command rejected label=%s attempt=%d/%d reply=NAK", label, attempt, c.maxAttempts)
			continue
		}
		c.closeConn()
		lastErr = fmt.Errorf("%w: unexpected reply %q", ErrAVCommandRejected, reply)
		logger.Debugf(tag, "command rejected label=%s attempt=%d/%d reply=%q", label, attempt, c.maxAttempts, reply)
	}
	logger.Warnf(tag, "command failed label=%s attempts=%d err=%v", label, c.maxAttempts, lastErr)
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
