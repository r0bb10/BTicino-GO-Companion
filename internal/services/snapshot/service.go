package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bticino-go-companion/internal/config"
	"bticino-go-companion/internal/domain/entrypoint"
	"bticino-go-companion/internal/logger"
	"bticino-go-companion/internal/services/media"
	"bticino-go-companion/internal/services/state"
)

const tag = "services.snapshot"

const (
	defaultCaptureTimeout = 10 * time.Second
)

var (
	ErrEntrypointNotFound      = errors.New("entrypoint not found")
	ErrCapabilityNotEnabled    = errors.New("entrypoint capability not enabled")
	ErrSnapshotBusy            = errors.New("snapshot capture already in progress")
	ErrSnapshotUnavailable     = errors.New("snapshot service unavailable")
	ErrSnapshotTimeout         = errors.New("snapshot capture timed out")
	ErrSnapshotNotFound        = errors.New("snapshot not found")
	ErrActiveEntrypointBlocked = errors.New("another entrypoint stream is already active")
)

type streamDriver interface {
	StartForEntrypoint(ctx context.Context, entrypointID string, devAddr string) error
	StopForEntrypoint(ctx context.Context, entrypointID string) error
	Snapshot() media.Snapshot
}

type mirrorDriver interface {
	BeginSnapshotMirror() (int, func(), error)
}

type stateDriver interface {
	Snapshot() state.Snapshot
}

type captureSourceSelection struct {
	UseExisting bool
	Blocked     bool
	Mode        string
	Entrypoint  string
	Reason      string
}

type Service struct {
	stream         streamDriver
	mirror         mirrorDriver
	state          stateDriver
	snapshotsDir   string
	entrypoints    map[string]entrypoint.Model
	captureTimeout time.Duration
	gstBinary      string

	captureMu sync.Mutex
}

func New(cfg config.Config, stream streamDriver, mirror mirrorDriver) *Service {
	return NewWithState(cfg, stream, mirror, nil)
}

func NewWithState(cfg config.Config, stream streamDriver, mirror mirrorDriver, state stateDriver) *Service {
	index := make(map[string]entrypoint.Model, len(cfg.Entrypoints))
	for _, ep := range cfg.Entrypoints {
		id := strings.TrimSpace(ep.ID)
		if id == "" {
			continue
		}
		index[id] = ep
	}
	svc := &Service{
		stream:         stream,
		mirror:         mirror,
		state:          state,
		snapshotsDir:   filepath.Join(cfg.DataDir, "media", "snapshots"),
		entrypoints:    index,
		captureTimeout: defaultCaptureTimeout,
		gstBinary:      "gst-launch-1.0",
	}
	logger.Infof(tag, "service ready entrypoints=%d dir=%s timeout=%s", len(index), svc.snapshotsDir, svc.captureTimeout)
	return svc
}

func (s *Service) Capture(ctx context.Context, entrypointID string) ([]byte, error) {
	ep, err := s.entrypoint(entrypointID)
	if err != nil {
		logger.Warnf(tag, "capture rejected entrypoint=%s err=%v", strings.TrimSpace(entrypointID), err)
		return nil, err
	}
	if s.stream == nil || s.mirror == nil {
		logger.Warnf(tag, "capture unavailable entrypoint=%s stream_set=%t mirror_set=%t", ep.ID, s.stream != nil, s.mirror != nil)
		return nil, ErrSnapshotUnavailable
	}
	if !s.captureMu.TryLock() {
		logger.Warnf(tag, "capture skipped entrypoint=%s reason=busy", ep.ID)
		return nil, ErrSnapshotBusy
	}
	defer s.captureMu.Unlock()
	logger.Infof(tag, "capture starting entrypoint=%s", ep.ID)

	if err := os.MkdirAll(s.snapshotsDir, 0o755); err != nil {
		logger.Errorf(tag, "capture failed entrypoint=%s step=prepare_dir err=%v", ep.ID, err)
		return nil, fmt.Errorf("prepare snapshot directory: %w", err)
	}

	before := s.stream.Snapshot()
	selection := s.selectCaptureSource(ep.ID, before)
	if selection.Blocked {
		logger.Warnf(tag, "capture blocked entrypoint=%s active_entrypoint=%s mode=%s reason=%s", ep.ID, selection.Entrypoint, selection.Mode, selection.Reason)
		return nil, ErrActiveEntrypointBlocked
	}

	port, stopMirror, err := s.mirror.BeginSnapshotMirror()
	if err != nil {
		logger.Warnf(tag, "capture failed entrypoint=%s step=begin_mirror err=%v", ep.ID, err)
		return nil, err
	}
	defer stopMirror()
	logger.Debugf(tag, "capture mirror ready entrypoint=%s port=%d", ep.ID, port)

	finalPath := s.pathForEntrypoint(ep.ID)
	tmpPath := finalPath + ".tmp"
	_ = os.Remove(tmpPath)

	captureCtx, cancel := context.WithTimeout(ctx, s.captureTimeout)
	defer cancel()

	startedBySnapshot := false
	var pipelineErr error
	if selection.UseExisting {
		logger.Debugf(tag, "capture using existing media entrypoint=%s active_entrypoint=%s mode=%s reason=%s", ep.ID, selection.Entrypoint, selection.Mode, selection.Reason)
		pipelineErr = s.runCapturePipeline(captureCtx, port, tmpPath, nil)
	} else {
		pipelineDone, err := s.startCapturePipeline(captureCtx, port, tmpPath)
		if err != nil {
			pipelineErr = err
		} else if err := s.stream.StartForEntrypoint(ctx, ep.ID, ep.DevAddr); err != nil {
			cancel()
			<-pipelineDone
			logger.Warnf(tag, "capture failed entrypoint=%s step=start_stream err=%v", ep.ID, err)
			return nil, err
		} else {
			startedBySnapshot = true
			logger.Debugf(tag, "capture started stream entrypoint=%s", ep.ID)
			pipelineErr = <-pipelineDone
		}
	}
	if startedBySnapshot {
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.stream.StopForEntrypoint(stopCtx, ep.ID); err != nil {
				logger.Warnf(tag, "stream stop failed entrypoint=%s err=%v", ep.ID, err)
			}
		}()
	}
	if pipelineErr != nil {
		if errors.Is(pipelineErr, context.DeadlineExceeded) || errors.Is(captureCtx.Err(), context.DeadlineExceeded) {
			logger.Warnf(tag, "capture timed out entrypoint=%s timeout=%s", ep.ID, s.captureTimeout)
			return nil, ErrSnapshotTimeout
		}
		logger.Warnf(tag, "capture failed entrypoint=%s step=pipeline err=%v", ep.ID, pipelineErr)
		return nil, pipelineErr
	}

	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() <= 0 {
		logger.Warnf(tag, "capture failed entrypoint=%s step=stat_tmp err=%v", ep.ID, err)
		return nil, ErrSnapshotTimeout
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		logger.Errorf(tag, "capture failed entrypoint=%s step=publish err=%v", ep.ID, err)
		return nil, fmt.Errorf("publish snapshot: %w", err)
	}
	image, err := os.ReadFile(finalPath)
	if err != nil {
		logger.Errorf(tag, "capture failed entrypoint=%s step=read_final err=%v", ep.ID, err)
		return nil, err
	}
	logger.Infof(tag, "capture complete entrypoint=%s bytes=%d path=%s", ep.ID, len(image), finalPath)
	return image, nil
}

func (s *Service) Latest(entrypointID string) (string, error) {
	ep, err := s.entrypoint(entrypointID)
	if err != nil {
		return "", err
	}
	path := s.pathForEntrypoint(ep.ID)
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		logger.Debugf(tag, "latest not found entrypoint=%s path=%s err=%v", ep.ID, path, err)
		return "", ErrSnapshotNotFound
	}
	return path, nil
}

func (s *Service) startCapturePipeline(ctx context.Context, port int, outputPath string) (<-chan error, error) {
	started := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.runCapturePipeline(ctx, port, outputPath, started)
	}()
	select {
	case err := <-started:
		if err != nil {
			<-done
			return nil, err
		}
		return done, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) runCapturePipeline(ctx context.Context, port int, outputPath string, started chan<- error) error {
	caps := "application/x-rtp,media=video,encoding-name=H264,payload=96,clock-rate=90000"
	args := []string{
		"-q",
		"udpsrc", fmt.Sprintf("port=%d", port), fmt.Sprintf("caps=%s", caps),
		"!",
		"rtph264depay",
		"!",
		"h264parse", "config-interval=-1",
		"!",
		"imxvpudec",
		"!",
		"videoconvert",
		"!",
		"jpegenc", "quality=90",
		"!",
		"filesink", "sync=false", fmt.Sprintf("location=%s", outputPath),
	}
	cmd := exec.CommandContext(ctx, s.gstBinary, args...)
	logger.Debugf(tag, "pipeline starting port=%d output=%s binary=%s", port, outputPath, s.gstBinary)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		if started != nil {
			started <- err
		}
		return fmt.Errorf("start snapshot pipeline: %w", err)
	}
	if started != nil {
		started <- nil
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	poll := time.NewTicker(30 * time.Millisecond)
	defer poll.Stop()

	captured := false
	for {
		select {
		case err := <-waitCh:
			if captured {
				ok, _ := isCompleteJPEG(outputPath)
				if ok {
					return nil
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return fmt.Errorf("snapshot pipeline failed: %w output=%s", err, strings.TrimSpace(output.String()))
			}
			ok, checkErr := isCompleteJPEG(outputPath)
			if checkErr == nil && ok {
				return nil
			}
			return fmt.Errorf("snapshot pipeline ended before jpeg completion output=%s", strings.TrimSpace(output.String()))
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-waitCh
			logger.Debugf(tag, "pipeline stopped by context output=%s err=%v", outputPath, ctx.Err())
			return ctx.Err()
		case <-poll.C:
			ok, err := isCompleteJPEG(outputPath)
			if err != nil || !ok {
				continue
			}
			captured = true
			logger.Debugf(tag, "pipeline captured complete jpeg output=%s", outputPath)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	}
}

func (s *Service) pathForEntrypoint(entrypointID string) string {
	return filepath.Join(s.snapshotsDir, fmt.Sprintf("%s.jpg", entrypointID))
}

func (s *Service) entrypoint(id string) (entrypoint.Model, error) {
	normalized := strings.TrimSpace(id)
	if normalized == "" {
		return entrypoint.Model{}, ErrEntrypointNotFound
	}
	ep, ok := s.entrypoints[normalized]
	if !ok {
		return entrypoint.Model{}, ErrEntrypointNotFound
	}
	if !ep.HasStream {
		return entrypoint.Model{}, ErrCapabilityNotEnabled
	}
	return ep, nil
}

func (s *Service) selectCaptureSource(entrypointID string, mediaSnapshot media.Snapshot) captureSourceSelection {
	entrypointID = strings.TrimSpace(entrypointID)
	if s.state != nil {
		stateSnapshot := s.state.Snapshot()
		mode := strings.TrimSpace(stateSnapshot.StreamState)
		activeEntrypoint := strings.TrimSpace(stateSnapshot.ActiveEntrypoint)
		switch mode {
		case state.StreamStatePreview, state.StreamStateActive:
			if activeEntrypoint != "" && activeEntrypoint != "none" {
				if activeEntrypoint != entrypointID {
					return captureSourceSelection{Blocked: true, Mode: mode, Entrypoint: activeEntrypoint, Reason: "state_active_entrypoint_mismatch"}
				}
				return captureSourceSelection{UseExisting: true, Mode: mode, Entrypoint: activeEntrypoint, Reason: "state_media_active"}
			}
		}
	}

	activeEntrypoint := strings.TrimSpace(mediaSnapshot.ActiveEntrypoint)
	if mediaSnapshot.StreamActive {
		if activeEntrypoint != "" && activeEntrypoint != entrypointID {
			return captureSourceSelection{Blocked: true, Mode: "media", Entrypoint: activeEntrypoint, Reason: "media_active_entrypoint_mismatch"}
		}
		return captureSourceSelection{UseExisting: true, Mode: "media", Entrypoint: activeEntrypoint, Reason: "media_stream_active"}
	}

	return captureSourceSelection{UseExisting: false, Mode: state.StreamStateIdle, Reason: "idle"}
}

func isCompleteJPEG(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() < 4 {
		return false, nil
	}

	var start [2]byte
	if _, err := io.ReadFull(f, start[:]); err != nil {
		return false, err
	}
	if start[0] != 0xFF || start[1] != 0xD8 {
		return false, nil
	}

	if _, err := f.Seek(-2, io.SeekEnd); err != nil {
		return false, err
	}
	var end [2]byte
	if _, err := io.ReadFull(f, end[:]); err != nil {
		return false, err
	}
	if end[0] != 0xFF || end[1] != 0xD9 {
		return false, nil
	}
	return true, nil
}
