package subspacerelay

import (
	"context"
	"errors"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nvx/go-rfid"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"google.golang.org/protobuf/types/known/emptypb"
	"log/slog"
	"slices"
)

// PCSC allows communicating with a remote PCSC-like connected reader
type PCSC struct {
	relay *SubspaceRelay

	RelayInfo *subspacerelaypb.RelayInfo
}

func NewPCSC(ctx context.Context, brokerURL, relayID string, connTypes ...subspacerelaypb.ConnectionType) (_ *PCSC, err error) {
	defer rfid.DeferWrap(ctx, &err)

	relay, err := New(ctx, brokerURL, relayID)
	if err != nil {
		return
	}
	p := &PCSC{
		relay: relay,
	}

	relay.RegisterHandler(p)

	slog.InfoContext(ctx, "Requesting relay info")
	msg, err := relay.Exchange(ctx, &subspacerelaypb.Message{
		Message: &subspacerelaypb.Message_RequestRelayInfo{RequestRelayInfo: &emptypb.Empty{}},
	})
	if err != nil {
		_ = p.Close()
		return
	}

	switch msg := msg.Message.(type) {
	case *subspacerelaypb.Message_RelayInfo:
		p.RelayInfo = msg.RelayInfo
		slog.InfoContext(ctx, "Got relay info", RelayInfoAttrs(p.RelayInfo))
		if len(connTypes) != 0 && !slices.Contains(connTypes, p.RelayInfo.ConnectionType) {
			err = errors.New("relay type mismatch")
			break
		}
	default:
		err = errors.New("unexpected response to request relay info message")
	}
	if err != nil {
		_ = p.Close()
		return
	}

	return p, nil
}

func (p *PCSC) Close() (err error) {
	return p.relay.conn.Disconnect(context.Background())
}

func (p *PCSC) Exchange(ctx context.Context, capdu []byte) ([]byte, error) {
	return p.relay.ExchangePayloadBytes(ctx, &subspacerelaypb.Payload{
		Payload:     capdu,
		PayloadType: subspacerelaypb.PayloadType_PAYLOAD_TYPE_PCSC_READER,
	})
}

func (p *PCSC) Control(ctx context.Context, code uint16, data []byte) ([]byte, error) {
	codeUint32 := uint32(code)
	return p.relay.ExchangePayloadBytes(ctx, &subspacerelaypb.Payload{
		Payload:     data,
		PayloadType: subspacerelaypb.PayloadType_PAYLOAD_TYPE_PCSC_READER_CONTROL,
		Control:     &codeUint32,
	})
}

func (p *PCSC) DeviceName() string {
	return p.RelayInfo.DeviceName
}

func (p *PCSC) ATR() ([]byte, error) {
	if p.RelayInfo.ConnectionType != subspacerelaypb.ConnectionType_CONNECTION_TYPE_PCSC {
		return nil, errors.New("atr not supported by connection type")
	}
	return p.RelayInfo.Atr, nil
}

func (p *PCSC) Reconnect(ctx context.Context) error {
	// TODO: Could add this
	return errors.New("unsupported")
}

func (p *PCSC) HandleMQTT(ctx context.Context, r *SubspaceRelay, pub *paho.Publish) bool {
	if len(pub.Properties.CorrelationData) != 0 {
		// ignore RPC replies
		return false
	}

	req, err := p.relay.Parse(ctx, pub)
	if err != nil {
		slog.ErrorContext(ctx, "Error parsing unsolicited message", rfid.ErrorAttrs(err))
		return false
	}

	switch msg := req.Message.(type) {
	case *subspacerelaypb.Message_Log:
		slog.InfoContext(ctx, "Remote Log: "+msg.Log.Message)
		return true
	default:
		return false
	}
}
