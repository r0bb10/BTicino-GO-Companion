package rtspadapter

import (
	"net"
	"testing"
	"time"
)

func TestAudioRTPMirrorCopiesValidatedSpeexDatagram(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	mirror := newAudioRTPMirror()
	status, err := mirror.Configure("speex", "speex", rtpPayloadTypeSpeex, listener.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !status.Enabled || status.Format != "speex" || status.PayloadType != rtpPayloadTypeSpeex {
		t.Fatalf("unexpected status: %+v", status)
	}

	datagram := []byte{0x80, rtpPayloadTypeSpeex, 0x12, 0x34, 0, 0, 0, 1, 0, 0, 0, 2, 0xaa, 0xbb}
	mirror.Write("speex", rtpPayloadTypeSpeex, datagram)

	buf := make([]byte, 64)
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	n, _, err := listener.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read mirrored datagram: %v", err)
	}
	if got := string(buf[:n]); got != string(datagram) {
		t.Fatalf("mirrored datagram changed: %x", buf[:n])
	}

	mirror.Write("speex", 96, datagram)
	if err := listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, _, err := listener.ReadFromUDP(buf); err == nil {
		t.Fatal("unexpected datagram with incorrect payload type")
	}

	status = mirror.Clear()
	if status.Enabled {
		t.Fatalf("mirror remained enabled: %+v", status)
	}
}
