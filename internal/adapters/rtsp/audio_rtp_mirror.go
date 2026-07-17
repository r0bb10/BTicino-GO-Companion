package rtspadapter

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"time"
)

type AudioRTPMirrorStatus struct {
	Enabled         bool       `json:"enabled"`
	Format          string     `json:"format,omitempty"`
	Codec           string     `json:"codec,omitempty"`
	PayloadType     uint8      `json:"payload_type,omitempty"`
	Destination     string     `json:"destination,omitempty"`
	MirroredPackets uint64     `json:"mirrored_packets"`
	MirroredBytes   uint64     `json:"mirrored_bytes"`
	WriteErrors     uint64     `json:"write_errors"`
	LastPacketAt    *time.Time `json:"last_packet_at,omitempty"`
}

type audioRTPMirror struct {
	mu sync.Mutex

	conn           *net.UDPConn
	status         AudioRTPMirrorStatus
	expectedFormat string
	expectedPT     uint8
}

func newAudioRTPMirror() *audioRTPMirror {
	return &audioRTPMirror{}
}

func (m *audioRTPMirror) Configure(format string, codec string, payloadType uint8, port int) (AudioRTPMirrorStatus, error) {
	if port < 1 || port > 65535 {
		return m.Status(), errors.New("mirror port must be between 1 and 65535")
	}

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return m.Status(), err
	}

	m.mu.Lock()
	oldConn := m.conn
	m.conn = conn
	m.expectedFormat = format
	m.expectedPT = payloadType
	m.status = AudioRTPMirrorStatus{
		Enabled:     true,
		Format:      format,
		Codec:       codec,
		PayloadType: payloadType,
		Destination: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	}
	status := m.status
	m.mu.Unlock()
	if oldConn != nil {
		_ = oldConn.Close()
	}
	return status, nil
}

func (m *audioRTPMirror) Clear() AudioRTPMirrorStatus {
	m.mu.Lock()
	conn := m.conn
	m.conn = nil
	m.expectedFormat = ""
	m.expectedPT = 0
	m.status = AudioRTPMirrorStatus{}
	status := m.status
	m.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	return status
}

func (m *audioRTPMirror) Status() AudioRTPMirrorStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *audioRTPMirror) Write(format string, payloadType uint8, datagram []byte) {
	if len(datagram) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil || format != m.expectedFormat || payloadType != m.expectedPT {
		return
	}
	if _, err := m.conn.Write(datagram); err != nil {
		m.status.WriteErrors++
		return
	}
	now := time.Now().UTC()
	m.status.MirroredPackets++
	m.status.MirroredBytes += uint64(len(datagram))
	m.status.LastPacketAt = &now
}
