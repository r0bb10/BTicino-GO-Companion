package webrtc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"bticino-go-companion/internal/domain/entrypoint"
	"bticino-go-companion/internal/logger"
	"bticino-go-companion/internal/system"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const tag = "services.webrtc"

const (
	defaultGatherTimeout = 5 * time.Second
	defaultAnswerTimeout = 8 * time.Second

	pendingCandidateTTL         = 45 * time.Second
	maxPendingSessionCandidates = 64
)

var webrtcICEPort = 8555
var preferredOutboundInterface = system.PreferredOutboundInterface

var (
	ErrSessionIDRequired  = errors.New("session_id is required")
	ErrEntrypointRequired = errors.New("entrypoint_id is required")
	ErrOfferRequired      = errors.New("offer_sdp is required")
	ErrSessionExists      = errors.New("session already exists")
	ErrSessionNotFound    = errors.New("session not found")
	ErrEntrypointNotFound = errors.New("entrypoint not found")
	ErrCandidateRequired  = errors.New("candidate is required")
)

type StreamLifecycle interface {
	ReaderJoin(ctx context.Context, sessionID string, entrypointID string, devAddr string) error
	ReaderLeave(ctx context.Context, sessionID string) error
}

type BackchannelWriter interface {
	WriteBackchannelOpus(pkt *rtp.Packet) error
	OpusPayloadType() uint8
	BackchannelOpusPayloadType() uint8
}

type Candidate struct {
	Candidate        string  `json:"candidate"`
	SDPMid           *string `json:"sdpMid,omitempty"`
	SDPMLineIndex    *uint16 `json:"sdpMLineIndex,omitempty"`
	UsernameFragment *string `json:"usernameFragment,omitempty"`
}

type OfferResult struct {
	SessionID    string      `json:"session_id"`
	EntrypointID string      `json:"entrypoint_id"`
	AnswerSDP    string      `json:"answer_sdp"`
	Candidates   []Candidate `json:"candidates,omitempty"`
}

type pendingCandidateBatch struct {
	candidates []webrtc.ICECandidateInit
	updatedAt  time.Time
}

type Service struct {
	stream      StreamLifecycle
	backchannel BackchannelWriter

	mu                sync.RWMutex
	sessions          map[string]*session
	pendingCandidates map[string]pendingCandidateBatch
	devAddrMap        map[string]string
	api               *webrtc.API
	videoPacketCount  int
	audioPacketCount  int
	lastVideoCountLog time.Time
	lastAudioCountLog time.Time
	iceServers        []string
}

type session struct {
	id           string
	entrypointID string
	devAddr      string

	pc         *webrtc.PeerConnection
	videoTrack *webrtc.TrackLocalStaticRTP
	audioTrack *webrtc.TrackLocalStaticRTP

	mu                   sync.Mutex
	candidates           []Candidate
	pendingRemoteICE     []webrtc.ICECandidateInit
	remoteDescriptionSet bool
	closeOnce            sync.Once
}

func New(stream StreamLifecycle, backchannel BackchannelWriter, entrypoints []entrypoint.Model, iceServers []string) (*Service, error) {
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
			RTCPFeedback: nil,
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, fmt.Errorf("register h264 codec: %w", err)
	}

	opusPayloadType := uint8(111)
	if backchannel != nil && backchannel.OpusPayloadType() > 0 {
		opusPayloadType = backchannel.OpusPayloadType()
	}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=0",
		},
		PayloadType: webrtc.PayloadType(opusPayloadType),
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register opus codec: %w", err)
	}

	iceConn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", webrtcICEPort))
	if err != nil {
		return nil, fmt.Errorf("listen webrtc ice udp %d: %w", webrtcICEPort, err)
	}
	udpMux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: iceConn})
	iface, _, err := preferredOutboundInterface()
	if err != nil {
		return nil, fmt.Errorf("select webrtc interface: %w", err)
	}

	se := webrtc.SettingEngine{}
	se.SetICEUDPMux(udpMux)
	se.SetInterfaceFilter(func(name string) bool {
		return name == iface.Name
	})
	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(me),
		webrtc.WithSettingEngine(se),
	)

	devAddrMap := make(map[string]string, len(entrypoints))
	for _, ep := range entrypoints {
		id := strings.TrimSpace(ep.ID)
		if id == "" || !ep.HasStream {
			continue
		}
		devAddrMap[id] = strings.TrimSpace(ep.DevAddr)
	}

	return &Service{
		stream:            stream,
		backchannel:       backchannel,
		sessions:          map[string]*session{},
		pendingCandidates: map[string]pendingCandidateBatch{},
		devAddrMap:        devAddrMap,
		api:               api,
		iceServers:        iceServers,
	}, nil
}

func (s *Service) HandleOffer(ctx context.Context, sessionID string, entrypointID string, offerSDP string) (OfferResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	entrypointID = strings.TrimSpace(entrypointID)
	offerSDP = canonicalizeSDP(offerSDP)
	logger.Infof(tag, "offer request session=%s entrypoint=%s offer_len=%d", sessionID, entrypointID, len(offerSDP))

	if sessionID == "" {
		logger.Warnf(tag, "offer rejected reason=missing_session")
		return OfferResult{}, ErrSessionIDRequired
	}
	if entrypointID == "" {
		logger.Warnf(tag, "offer rejected session=%s reason=missing_entrypoint", sessionID)
		return OfferResult{}, ErrEntrypointRequired
	}
	if offerSDP == "" {
		logger.Warnf(tag, "offer rejected session=%s entrypoint=%s reason=missing_offer", sessionID, entrypointID)
		return OfferResult{}, ErrOfferRequired
	}

	devAddr, ok := s.devAddrMap[entrypointID]
	if !ok {
		logger.Warnf(tag, "offer rejected session=%s entrypoint=%s reason=entrypoint_not_found", sessionID, entrypointID)
		return OfferResult{}, ErrEntrypointNotFound
	}

	s.mu.Lock()
	s.prunePendingCandidatesLocked(time.Now())
	if _, exists := s.sessions[sessionID]; exists {
		s.mu.Unlock()
		logger.Warnf(tag, "offer rejected session=%s reason=session_exists", sessionID)
		return OfferResult{}, ErrSessionExists
	}
	s.mu.Unlock()

	offerCtx, cancel := context.WithTimeout(ctx, defaultAnswerTimeout)
	defer cancel()

	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
	}, "video", sessionID)
	if err != nil {
		return OfferResult{}, fmt.Errorf("create video track: %w", err)
	}
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeOpus,
		ClockRate:   48000,
		Channels:    2,
		SDPFmtpLine: "minptime=10;useinbandfec=0",
	}, "audio", sessionID)
	if err != nil {
		return OfferResult{}, fmt.Errorf("create audio track: %w", err)
	}
	offeredAudioDirection := offeredAudioDirection(offerSDP)
	audioDirection := answerDirectionForOfferedAudio(offeredAudioDirection)
	logger.Infof(tag, "audio direction session=%s offered=%s answer=%s", sessionID, offeredAudioDirection.String(), audioDirection.String())

	cfg := webrtc.Configuration{}
	if len(s.iceServers) > 0 {
		ices := make([]webrtc.ICEServer, len(s.iceServers))
		for i, url := range s.iceServers {
			ices[i] = webrtc.ICEServer{URLs: []string{url}}
		}
		cfg.ICEServers = ices
	}
	pc, err := s.api.NewPeerConnection(cfg)
	if err != nil {
		return OfferResult{}, fmt.Errorf("create peer connection: %w", err)
	}

	sess := &session{
		id:           sessionID,
		entrypointID: entrypointID,
		devAddr:      devAddr,
		pc:           pc,
		videoTrack:   videoTrack,
		audioTrack:   audioTrack,
	}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		val := c.ToJSON()
		sess.mu.Lock()
		sess.candidates = append(sess.candidates, Candidate{
			Candidate:        val.Candidate,
			SDPMid:           val.SDPMid,
			SDPMLineIndex:    val.SDPMLineIndex,
			UsernameFragment: val.UsernameFragment,
		})
		sess.mu.Unlock()
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.Infof(tag, "connection state session=%s state=%s", sess.id, state.String())
		switch state {
		case webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateFailed:
			s.closeSession(sess.id)
		case webrtc.PeerConnectionStateDisconnected:
			go func(sessionID string, pc *webrtc.PeerConnection) {
				time.Sleep(5 * time.Second)
				if pc.ConnectionState() == webrtc.PeerConnectionStateDisconnected {
					s.closeSession(sessionID)
				}
			}(sess.id, pc)
		}
	})

	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track == nil {
			logger.Debugf(tag, "remote track ignored session=%s reason=nil_track", sess.id)
			return
		}
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			logger.Debugf(tag, "remote track ignored session=%s kind=%s reason=not_audio", sess.id, track.Kind().String())
			return
		}
		if s.backchannel == nil {
			logger.Warnf(tag, "remote audio track ignored session=%s reason=backchannel_unavailable", sess.id)
			return
		}
		logger.Debugf(tag, "remote audio track accepted session=%s", sess.id)
		go s.forwardBackchannel(track)
	})

	videoTransceiver, err := pc.AddTransceiverFromTrack(videoTrack, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	})
	if err != nil {
		_ = pc.Close()
		logger.Warnf(tag, "add video transceiver failed session=%s err=%v", sessionID, err)
		return OfferResult{}, fmt.Errorf("add video transceiver: %w", err)
	}
	audioTransceiver, err := pc.AddTransceiverFromTrack(audioTrack, webrtc.RTPTransceiverInit{
		Direction: audioDirection,
	})
	if err != nil {
		_ = pc.Close()
		logger.Warnf(tag, "add audio transceiver failed session=%s err=%v", sessionID, err)
		return OfferResult{}, fmt.Errorf("add audio transceiver: %w", err)
	}
	go drainRTCP(videoTransceiver.Sender())
	go drainRTCP(audioTransceiver.Sender())

	s.mu.Lock()
	if _, exists := s.sessions[sessionID]; exists {
		s.mu.Unlock()
		_ = pc.Close()
		logger.Warnf(tag, "offer rejected session=%s reason=session_race", sessionID)
		return OfferResult{}, ErrSessionExists
	}
	if pending, ok := s.pendingCandidates[sessionID]; ok && len(pending.candidates) > 0 {
		sess.pendingRemoteICE = append(sess.pendingRemoteICE, pending.candidates...)
		delete(s.pendingCandidates, sessionID)
		logger.Debugf(tag, "moved pending candidates session=%s count=%d", sessionID, len(pending.candidates))
	}
	s.sessions[sessionID] = sess
	s.mu.Unlock()
	joined := false
	defer func() {
		if joined {
			_ = s.stream.ReaderLeave(context.Background(), sessionID)
		}
	}()

	if err := s.stream.ReaderJoin(offerCtx, sessionID, entrypointID, devAddr); err != nil {
		s.closeSession(sessionID)
		logger.Warnf(tag, "reader join failed session=%s entrypoint=%s devaddr=%s err=%v", sessionID, entrypointID, devAddr, err)
		return OfferResult{}, err
	}
	joined = true
	logger.Infof(tag, "reader joined session=%s entrypoint=%s devaddr=%s", sessionID, entrypointID, devAddr)

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}
	if err := pc.SetRemoteDescription(offer); err != nil {
		s.closeSession(sessionID)
		logger.Warnf(tag, "set remote description failed session=%s err=%v", sessionID, err)
		return OfferResult{}, fmt.Errorf("set remote description: %w", err)
	}
	logger.Debugf(tag, "remote description set session=%s", sessionID)
	if err := s.flushPendingRemoteCandidates(sess); err != nil {
		s.closeSession(sessionID)
		logger.Warnf(tag, "apply queued candidates failed session=%s err=%v", sessionID, err)
		return OfferResult{}, fmt.Errorf("apply queued candidates: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		s.closeSession(sessionID)
		logger.Warnf(tag, "create answer failed session=%s err=%v", sessionID, err)
		return OfferResult{}, fmt.Errorf("create answer: %w", err)
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		s.closeSession(sessionID)
		logger.Warnf(tag, "set local description failed session=%s err=%v", sessionID, err)
		return OfferResult{}, fmt.Errorf("set local description: %w", err)
	}

	select {
	case <-gatherDone:
	case <-offerCtx.Done():
		s.closeSession(sessionID)
		logger.Warnf(tag, "ice gathering cancelled session=%s err=%v", sessionID, offerCtx.Err())
		return OfferResult{}, offerCtx.Err()
	case <-time.After(defaultGatherTimeout):
		logger.Warnf(tag, "ice gathering timed out session=%s timeout=%s", sessionID, defaultGatherTimeout)
	}

	local := pc.LocalDescription()
	if local == nil || strings.TrimSpace(local.SDP) == "" {
		s.closeSession(sessionID)
		logger.Warnf(tag, "local answer missing session=%s", sessionID)
		return OfferResult{}, errors.New("local answer not available")
	}

	sess.mu.Lock()
	candidates := append([]Candidate(nil), sess.candidates...)
	sess.mu.Unlock()
	joined = false
	logger.Infof(tag, "offer answered session=%s entrypoint=%s candidates=%d answer_len=%d", sessionID, entrypointID, len(candidates), len(local.SDP))

	return OfferResult{
		SessionID:    sessionID,
		EntrypointID: entrypointID,
		AnswerSDP:    local.SDP,
		Candidates:   candidates,
	}, nil
}

func (s *Service) AddCandidate(sessionID string, candidate Candidate) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		logger.Warnf(tag, "candidate rejected reason=missing_session")
		return ErrSessionIDRequired
	}
	if strings.TrimSpace(candidate.Candidate) == "" {
		logger.Warnf(tag, "candidate rejected session=%s reason=missing_candidate", sessionID)
		return ErrCandidateRequired
	}
	logger.Debugf(tag, "candidate received session=%s mid_set=%v mline_set=%v", sessionID, candidate.SDPMid != nil, candidate.SDPMLineIndex != nil)

	init := webrtc.ICECandidateInit{
		Candidate:        candidate.Candidate,
		SDPMid:           candidate.SDPMid,
		SDPMLineIndex:    candidate.SDPMLineIndex,
		UsernameFragment: candidate.UsernameFragment,
	}

	s.mu.Lock()
	s.prunePendingCandidatesLocked(time.Now())
	sess := s.sessions[sessionID]
	if sess == nil {
		batch := s.pendingCandidates[sessionID]
		if len(batch.candidates) < maxPendingSessionCandidates {
			batch.candidates = append(batch.candidates, init)
		}
		batch.updatedAt = time.Now()
		s.pendingCandidates[sessionID] = batch
		s.mu.Unlock()
		logger.Debugf(tag, "candidate queued session=%s queued=%d", sessionID, len(batch.candidates))
		return nil
	}
	s.mu.Unlock()

	return s.addCandidateToSession(sess, init)
}

func (s *Service) addCandidateToSession(sess *session, candidate webrtc.ICECandidateInit) error {
	sess.mu.Lock()
	if !sess.remoteDescriptionSet {
		if len(sess.pendingRemoteICE) < maxPendingSessionCandidates {
			sess.pendingRemoteICE = append(sess.pendingRemoteICE, candidate)
		}
		queued := len(sess.pendingRemoteICE)
		sess.mu.Unlock()
		logger.Debugf(tag, "candidate pending_remote_description session=%s queued=%d", sess.id, queued)
		return nil
	}
	pc := sess.pc
	sess.mu.Unlock()

	if err := pc.AddICECandidate(candidate); err != nil {
		logger.Warnf(tag, "candidate apply failed session=%s err=%v", sess.id, err)
		return err
	}
	logger.Debugf(tag, "candidate applied session=%s", sess.id)
	return nil
}

func (s *Service) flushPendingRemoteCandidates(sess *session) error {
	if sess == nil || sess.pc == nil {
		return nil
	}

	sess.mu.Lock()
	sess.remoteDescriptionSet = true
	pending := append([]webrtc.ICECandidateInit(nil), sess.pendingRemoteICE...)
	sess.pendingRemoteICE = nil
	pc := sess.pc
	sess.mu.Unlock()

	for i, candidate := range pending {
		if err := pc.AddICECandidate(candidate); err != nil {
			logger.Warnf(tag, "queued candidate apply failed session=%s index=%d total=%d err=%v", sess.id, i+1, len(pending), err)
			return err
		}
	}
	if len(pending) > 0 {
		logger.Debugf(tag, "queued candidates applied session=%s count=%d", sess.id, len(pending))
	}
	return nil
}

func (s *Service) Close(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ErrSessionIDRequired
	}
	return s.closeSession(sessionID)
}

func (s *Service) closeSession(sessionID string) error {
	s.mu.RLock()
	sess := s.sessions[sessionID]
	s.mu.RUnlock()
	if sess == nil {
		s.mu.Lock()
		delete(s.pendingCandidates, sessionID)
		s.mu.Unlock()
		logger.Debugf(tag, "close noop session=%s reason=not_found", sessionID)
		return nil
	}
	logger.Infof(tag, "close session=%s entrypoint=%s devaddr=%s", sessionID, sess.entrypointID, sess.devAddr)

	sess.closeOnce.Do(func() {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		delete(s.pendingCandidates, sessionID)
		s.mu.Unlock()
		if err := sess.pc.Close(); err != nil {
			logger.Warnf(tag, "close peer connection session=%s err=%v", sessionID, err)
		}
		if s.stream != nil {
			if err := s.stream.ReaderLeave(context.Background(), sessionID); err != nil {
				logger.Warnf(tag, "reader leave failed session=%s err=%v", sessionID, err)
			}
		}
	})
	return nil
}

func (s *Service) WriteVideoRTP(pkt *rtp.Packet) {
	if pkt == nil {
		return
	}
	tracks := s.videoTracks()
	s.logVideoPacket(pkt, len(tracks))
	for _, tr := range tracks {
		if err := tr.WriteRTP(pkt); err != nil {
			logger.Debugf(tag, "video write failed err=%v", err)
		}
	}
}

func (s *Service) WriteAudioRTP(pkt *rtp.Packet) {
	if pkt == nil {
		return
	}
	tracks := s.audioTracks()
	s.logAudioPacket(pkt, len(tracks))
	for _, tr := range tracks {
		if err := tr.WriteRTP(pkt); err != nil {
			logger.Debugf(tag, "audio write failed err=%v", err)
		}
	}
}

func (s *Service) logVideoPacket(pkt *rtp.Packet, trackCount int) {
	s.mu.Lock()
	s.videoPacketCount++
	count := s.videoPacketCount
	shouldLog := count == 1 || time.Since(s.lastVideoCountLog) >= 5*time.Second
	if shouldLog {
		s.lastVideoCountLog = time.Now()
	}
	s.mu.Unlock()
	if shouldLog {
		logger.Debugf(tag, "video rtp count=%d tracks=%d pt=%d seq=%d ts=%d ssrc=%d", count, trackCount, pkt.PayloadType, pkt.SequenceNumber, pkt.Timestamp, pkt.SSRC)
	}
}

func (s *Service) logAudioPacket(pkt *rtp.Packet, trackCount int) {
	s.mu.Lock()
	s.audioPacketCount++
	count := s.audioPacketCount
	shouldLog := count == 1 || time.Since(s.lastAudioCountLog) >= 5*time.Second
	if shouldLog {
		s.lastAudioCountLog = time.Now()
	}
	s.mu.Unlock()
	if shouldLog {
		logger.Debugf(tag, "audio rtp count=%d tracks=%d pt=%d seq=%d ts=%d ssrc=%d", count, trackCount, pkt.PayloadType, pkt.SequenceNumber, pkt.Timestamp, pkt.SSRC)
	}
}

func (s *Service) videoTracks() []*webrtc.TrackLocalStaticRTP {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*webrtc.TrackLocalStaticRTP, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess.videoTrack != nil {
			out = append(out, sess.videoTrack)
		}
	}
	return out
}

func (s *Service) audioTracks() []*webrtc.TrackLocalStaticRTP {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*webrtc.TrackLocalStaticRTP, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess.audioTrack != nil {
			out = append(out, sess.audioTrack)
		}
	}
	return out
}

func (s *Service) forwardBackchannel(track *webrtc.TrackRemote) {
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				logger.Warnf(tag, "backchannel read failed err=%v", err)
			}
			return
		}
		if pkt == nil || s.backchannel == nil {
			continue
		}
		pkt.PayloadType = s.backchannel.BackchannelOpusPayloadType()
		if err := s.backchannel.WriteBackchannelOpus(pkt); err != nil {
			logger.Warnf(tag, "backchannel write failed err=%v", err)
		}
	}
}

func (s *Service) prunePendingCandidatesLocked(now time.Time) {
	if len(s.pendingCandidates) == 0 {
		return
	}
	for sessionID, batch := range s.pendingCandidates {
		if now.Sub(batch.updatedAt) > pendingCandidateTTL {
			delete(s.pendingCandidates, sessionID)
		}
	}
}

func drainRTCP(sender *webrtc.RTPSender) {
	if sender == nil {
		return
	}
	buf := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}

func canonicalizeSDP(raw string) string {
	text := strings.Trim(raw, " \t\r\n")
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.ReplaceAll(text, "\n", "\r\n") + "\r\n"
}

func answerDirectionFromOfferAudio(offerSDP string) webrtc.RTPTransceiverDirection {
	return answerDirectionForOfferedAudio(offeredAudioDirection(offerSDP))
}

func answerDirectionForOfferedAudio(offered webrtc.RTPTransceiverDirection) webrtc.RTPTransceiverDirection {
	switch offered {
	case webrtc.RTPTransceiverDirectionRecvonly:
		return webrtc.RTPTransceiverDirectionSendonly
	case webrtc.RTPTransceiverDirectionSendonly:
		return webrtc.RTPTransceiverDirectionRecvonly
	case webrtc.RTPTransceiverDirectionInactive:
		return webrtc.RTPTransceiverDirectionInactive
	case webrtc.RTPTransceiverDirectionSendrecv:
		return webrtc.RTPTransceiverDirectionSendrecv
	default:
		return webrtc.RTPTransceiverDirectionSendonly
	}
}

func offeredAudioDirection(offerSDP string) webrtc.RTPTransceiverDirection {
	text := canonicalizeSDP(offerSDP)
	if text == "" {
		return webrtc.RTPTransceiverDirectionSendrecv
	}
	lines := strings.Split(text, "\r\n")

	sessionDir := webrtc.RTPTransceiverDirectionSendrecv
	inAudio := false
	audioSeen := false
	audioDir := webrtc.RTPTransceiverDirectionUnknown

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "m=") {
			inAudio = strings.HasPrefix(line, "m=audio ")
			if inAudio {
				audioSeen = true
				audioDir = webrtc.RTPTransceiverDirectionUnknown
			} else if audioSeen {
				break
			}
			continue
		}

		if !strings.HasPrefix(line, "a=") {
			continue
		}

		dir := parseDirectionAttr(strings.TrimPrefix(line, "a="))
		if dir == webrtc.RTPTransceiverDirectionUnknown {
			continue
		}

		if inAudio {
			audioDir = dir
			continue
		}
		if !audioSeen {
			sessionDir = dir
		}
	}

	if !audioSeen {
		return webrtc.RTPTransceiverDirectionInactive
	}
	if audioDir != webrtc.RTPTransceiverDirectionUnknown {
		return audioDir
	}
	return sessionDir
}

func parseDirectionAttr(attr string) webrtc.RTPTransceiverDirection {
	switch strings.ToLower(strings.TrimSpace(attr)) {
	case "sendrecv":
		return webrtc.RTPTransceiverDirectionSendrecv
	case "sendonly":
		return webrtc.RTPTransceiverDirectionSendonly
	case "recvonly":
		return webrtc.RTPTransceiverDirectionRecvonly
	case "inactive":
		return webrtc.RTPTransceiverDirectionInactive
	default:
		return webrtc.RTPTransceiverDirectionUnknown
	}
}
