package openwebnet

import (
	"context"
	"errors"
	"fmt"
	"net"

	"bticino-go-companion/internal/domain/event"
	"bticino-go-companion/internal/logger"
	"bticino-go-companion/internal/protocol/openwebnet"
)

const listenerTag = "adapters.openwebnet.listener"

type Listener struct {
	group  string
	port   int
	buffer int
	parser *openwebnetproto.Parser
	mapper *openwebnetproto.Mapper

	traceSink func(openwebnetproto.Message, []event.Envelope)
}

func NewListener(group string, port int, buffer int) *Listener {
	if buffer <= 0 {
		buffer = 65535
	}
	return &Listener{
		group:  group,
		port:   port,
		buffer: buffer,
		parser: openwebnetproto.NewParser(),
		mapper: openwebnetproto.NewMapper(),
	}
}

func (l *Listener) SetTraceSink(sink func(openwebnetproto.Message, []event.Envelope)) {
	l.traceSink = sink
}

func (l *Listener) Run(ctx context.Context, sink func(event.Envelope)) error {
	ip := net.ParseIP(l.group)
	if ip == nil {
		logger.Errorf(listenerTag, "start rejected reason=invalid_multicast_group group=%s", l.group)
		return fmt.Errorf("invalid multicast group: %s", l.group)
	}
	addr := &net.UDPAddr{IP: ip, Port: l.port}
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		logger.Errorf(listenerTag, "listen failed group=%s port=%d err=%v", l.group, l.port, err)
		return err
	}
	defer conn.Close()
	if err := conn.SetReadBuffer(l.buffer); err != nil {
		logger.Warnf(listenerTag, "set read buffer failed bytes=%d err=%v", l.buffer, err)
	}
	logger.Infof(listenerTag, "listening group=%s port=%d buffer=%d", l.group, l.port, l.buffer)

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, l.buffer)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				logger.Infof(listenerTag, "stopped")
				return nil
			}
			logger.Warnf(listenerTag, "read failed err=%v", err)
			continue
		}
		msg, parseErr := l.parser.Parse(buf[:n])
		if parseErr != nil {
			logger.Debugf(listenerTag, "parse skipped bytes=%d err=%v", n, parseErr)
			continue
		}
		mapped := l.mapper.Map(msg)
		if len(mapped) == 0 {
			logger.Debugf(listenerTag, "frame ignored system=%s raw=%s", msg.System, msg.Raw)
		} else {
			logger.Debugf(listenerTag, "frame mapped system=%s events=%d raw=%s", msg.System, len(mapped), msg.Raw)
		}
		if l.traceSink != nil {
			l.traceSink(msg, mapped)
		}
		for _, ev := range mapped {
			sink(ev)
		}
	}
}
