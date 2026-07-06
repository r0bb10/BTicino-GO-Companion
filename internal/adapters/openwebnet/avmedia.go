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

	videoRecentlyFlowing func(time.Duration) bool
	audioRecentlyFlowing func(time.Duration) bool
	flowConfirmTimeout   time.Duration
	flowConfirmPoll      time.Duration
	flowRecentWindow     time.Duration

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
		retryDelay:   300 * time.Millisecond,
		maxAttempts:  3,
		audioDelay:   50 * time.Millisecond,

		flowConfirmTimeout: 1200 * time.Millisecond,
		flowConfirmPoll:    100 * time.Millisecond,
		flowRecentWindow:   2 * time.Second,
	}
}

func (c *AVMediaClient) SetVideoRecentlyFlowing(fn func(time.Duration) bool) {
	c.mu.Lock()
	c.videoRecentlyFlowing = fn
	c.mu.Unlock()
}

func (c *AVMediaClient) SetAudioRecentlyFlowing(fn func(time.Duration) bool) {
	c.mu.Lock()
	c.audioRecentlyFlowing = fn
	c.mu.Unlock()
}

func (c *AVMediaClient) StreamStart(ctx context.Context, audioPort, videoPort int) error {
	if audioPort <= 0 || videoPort <= 0 {
		logger.Warnf(tag, "stream start rejected reason=invalid_ports audio=%d video=%d", audioPort, videoPort)
		return errors.New("invalid av stream ports")
	}
	if c.isVideoFlowing() {
		logger.Infof(tag, "add-video-stream skipped reason=video_already_flowing audio_port=%d video_port=%d", audioPort, videoPort)
	} else {
		video := openwebnetproto.BuildAVAddStreamVideo(c.streamIP, videoPort, c.highRes)
		if err := c.sendCommand(ctx, "add-video-stream", video, c.isVideoFlowing); err != nil {
			if c.waitForFlow(ctx, c.isVideoFlowing) {
				logger.Warnf(tag, "add-video-stream accepted reason=video_flowing audio_port=%d video_port=%d err=%v", audioPort, videoPort, err)
			} else {
				logger.Errorf(tag, "add-video-stream failed audio_port=%d video_port=%d err=%v", audioPort, videoPort, err)
				return err
			}
		}
	}
	if !c.waitForFlow(ctx, c.isVideoFlowing) {
		err := errors.New("video rtp did not start")
		logger.Errorf(tag, "add-video-stream failed reason=video_not_flowing audio_port=%d video_port=%d", audioPort, videoPort)
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.audioDelay):
	}
	if c.isAudioFlowing() {
		logger.Infof(tag, "add-audio-stream skipped reason=audio_already_flowing audio_port=%d video_port=%d", audioPort, videoPort)
		logger.Infof(tag, "stream start complete audio_port=%d video_port=%d highres=%t", audioPort, videoPort, c.highRes)
		return nil
	}

	audio := openwebnetproto.BuildAVAddStreamAudio(c.streamIP, audioPort)
	if err := c.sendCommand(ctx, "add-audio-stream", audio, c.isAudioFlowing); err != nil {
		if c.waitForFlow(ctx, c.isAudioFlowing) {
			logger.Warnf(tag, "add-audio-stream accepted reason=audio_flowing audio_port=%d video_port=%d err=%v", audioPort, videoPort, err)
		} else {
			logger.Errorf(tag, "add-audio-stream failed audio_port=%d video_port=%d err=%v", audioPort, videoPort, err)
			return err
		}
	}
	if !c.waitForFlow(ctx, c.isAudioFlowing) {
		err := errors.New("audio rtp did not start")
		logger.Errorf(tag, "add-audio-stream failed reason=audio_not_flowing audio_port=%d video_port=%d", audioPort, videoPort)
		return err
	}
	logger.Infof(tag, "stream start complete audio_port=%d video_port=%d highres=%t", audioPort, videoPort, c.highRes)
	return nil
}

func (c *AVMediaClient) sendCommand(ctx context.Context, label, frame string, flowing func() bool) error {
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if flowing != nil && flowing() {
			logger.Debugf(tag, "command skipped label=%s reason=flowing attempt=%d/%d", label, attempt, c.maxAttempts)
			return nil
		}
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.retryDelay):
			}
			if flowing != nil && flowing() {
				logger.Debugf(tag, "command retry skipped label=%s reason=flowing attempt=%d/%d", label, attempt, c.maxAttempts)
				return nil
			}
		}
		reply, err := c.exchange(frame)
		if err != nil {
			lastErr = err
			logger.Debugf(tag, "command attempt failed label=%s attempt=%d/%d err=%v", label, attempt, c.maxAttempts, err)
			if flowing != nil && flowing() {
				logger.Debugf(tag, "command accepted label=%s reason=flowing_after_error attempt=%d/%d", label, attempt, c.maxAttempts)
				return nil
			}
			continue
		}
		logger.Debugf(tag, "command reply label=%s attempt=%d/%d frame=%q reply=%q", label, attempt, c.maxAttempts, frame, reply)
		if isAllACKs(reply) {
			return nil
		}
		if reply == openwebnetproto.FrameNACK {
			lastErr = fmt.Errorf("%w: NAK", ErrAVCommandRejected)
			logger.Debugf(tag, "command rejected label=%s attempt=%d/%d reply=NAK", label, attempt, c.maxAttempts)
			if flowing != nil && flowing() {
				logger.Debugf(tag, "command accepted label=%s reason=flowing_after_nak attempt=%d/%d", label, attempt, c.maxAttempts)
				return nil
			}
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
	reply := strings.TrimSpace(string(buf[:n]))
	c.closeConnLocked()
	return reply, nil
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

func (c *AVMediaClient) isVideoFlowing() bool {
	c.mu.Lock()
	fn := c.videoRecentlyFlowing
	window := c.flowRecentWindow
	c.mu.Unlock()
	if fn == nil {
		return false
	}
	return fn(window)
}

func (c *AVMediaClient) isAudioFlowing() bool {
	c.mu.Lock()
	fn := c.audioRecentlyFlowing
	window := c.flowRecentWindow
	c.mu.Unlock()
	if fn == nil {
		return false
	}
	return fn(window)
}

func (c *AVMediaClient) waitForFlow(ctx context.Context, flowing func() bool) bool {
	c.mu.Lock()
	timeout := c.flowConfirmTimeout
	poll := c.flowConfirmPoll
	c.mu.Unlock()

	if flowing == nil {
		return false
	}
	if flowing() {
		return true
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		if flowing() {
			return true
		}
		select {
		case <-deadlineCtx.Done():
			return false
		case <-ticker.C:
		}
	}
}
