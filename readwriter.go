package subspacerelay

import (
	"context"
	"errors"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nvx/go-rfid"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"google.golang.org/protobuf/types/known/emptypb"
	"io"
	"log/slog"
	"slices"
	"time"
)

type readWriter struct {
	r           *SubspaceRelay
	ctx         context.Context
	cancel      context.CancelFunc
	readChan    chan []byte
	readBuf     []byte
	payloadType subspacerelaypb.PayloadType
}

var (
	_ io.ReadWriteCloser = (*readWriter)(nil)
)

func (r *SubspaceRelay) ReadWriter(ctx context.Context, payloadType subspacerelaypb.PayloadType) (_ io.ReadWriter, err error) {
	ctx, cancel := context.WithCancel(ctx)

	rw := &readWriter{
		r:           r,
		ctx:         ctx,
		cancel:      cancel,
		readChan:    make(chan []byte, 1),
		payloadType: payloadType,
	}

	r.RegisterHandler(rw)

	if !r.isRelaySide {
		err = rw.checkClient(ctx)
		if err != nil {
			return
		}
	}

	return rw, nil
}

func (rw *readWriter) checkClient(ctx context.Context) (err error) {
	slog.InfoContext(ctx, "Requesting relay info")
	msg, err := rw.r.Exchange(ctx, &subspacerelaypb.Message{
		Message: &subspacerelaypb.Message_RequestRelayInfo{RequestRelayInfo: &emptypb.Empty{}},
	})
	if err != nil {
		return
	}

	switch msg := msg.Message.(type) {
	case *subspacerelaypb.Message_RelayInfo:
		slog.InfoContext(ctx, "Got relay info", RelayInfoAttrs(msg.RelayInfo))
		if msg.RelayInfo.ConnectionType != subspacerelaypb.ConnectionType_CONNECTION_TYPE_NFC ||
			!slices.Contains(msg.RelayInfo.SupportedPayloadTypes, subspacerelaypb.PayloadType_PAYLOAD_TYPE_CARDHOPPER) {
			err = errors.New("relay type mismatch")
			break
		}
	default:
		err = errors.New("unexpected response to request relay info message")
	}
	if err != nil {
		return
	}

	return nil
}

func (rw *readWriter) Close() error {
	rw.cancel()
	return nil
}

func (rw *readWriter) HandleMQTT(ctx context.Context, r *SubspaceRelay, p *paho.Publish) bool {
	if len(p.Properties.CorrelationData) != 0 {
		// ignore RPC replies
		return false
	}

	req, err := rw.r.Parse(rw.ctx, p)
	if err != nil {
		slog.ErrorContext(rw.ctx, "Error parsing message", rfid.ErrorAttrs(err))
		return false
	}

	switch msg := req.Message.(type) {
	case *subspacerelaypb.Message_Payload:
		if msg.Payload.PayloadType != rw.payloadType {
			return false
		}

		select {
		case rw.readChan <- msg.Payload.Payload:
			return true
		case <-rw.ctx.Done():
			return false
		}
	case *subspacerelaypb.Message_Log:
		slog.InfoContext(ctx, "Remote Log: "+msg.Log.Message)
		return true
	case *subspacerelaypb.Message_Disconnect:
		slog.InfoContext(ctx, "Remote disconnected")
		return true
	default:
		return false
	}
}

func (rw *readWriter) Read(p []byte) (_ int, err error) {
	if len(rw.readBuf) > 0 {
		n := copy(p, rw.readBuf)

		rw.readBuf = rw.readBuf[n:]

		if len(rw.readBuf) == 0 {
			rw.readBuf = nil
		}

		return n, nil
	}

	ctx, cancel := context.WithTimeout(rw.ctx, 200*time.Millisecond)
	defer cancel()

	select {
	case read := <-rw.readChan:
		n := copy(p, read)

		if len(read) > n {
			rw.readBuf = read[n:]
		}

		return n, nil
	case <-rw.ctx.Done():
		return 0, context.Cause(rw.ctx)
	case <-ctx.Done():
		return 0, nil
	}
}

func (rw *readWriter) Write(p []byte) (_ int, err error) {
	err = rw.r.SendUnsolicited(rw.ctx,
		&subspacerelaypb.Message{Message: &subspacerelaypb.Message_Payload{Payload: &subspacerelaypb.Payload{
			Payload:     p,
			PayloadType: rw.payloadType,
		}}},
	)
	if err != nil {
		return
	}

	return len(p), nil
}
