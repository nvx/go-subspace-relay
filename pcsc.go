package subspacerelay

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/eclipse/paho.golang/paho"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"google.golang.org/protobuf/types/known/emptypb"
	"log/slog"
	"slices"
	"strings"
)

// PCSC allows communicating with a remote PCSC-like connected reader
type PCSC struct {
	relay *SubspaceRelay

	ClientInfo *subspacerelaypb.ClientInfo
}

func NewPCSC(ctx context.Context, serverURL, relayID string, connTypes ...subspacerelaypb.ConnectionType) (_ *PCSC, err error) {
	defer DeferWrap(&err)

	relay, err := New(ctx, serverURL, relayID)
	if err != nil {
		return
	}
	p := &PCSC{
		relay: relay,
	}

	relay.RegisterHandler(p)

	slog.InfoContext(ctx, "Requesting client info")
	msg, err := relay.Exchange(ctx, &subspacerelaypb.Message{
		Message: &subspacerelaypb.Message_RequestClientInfo{RequestClientInfo: &emptypb.Empty{}},
	})
	if err != nil {
		_ = p.Close()
		return
	}

	switch msg := msg.Message.(type) {
	case *subspacerelaypb.Message_ClientInfo:
		p.ClientInfo = msg.ClientInfo
		slog.InfoContext(ctx, "Got client info", slog.String("connection_type", p.ClientInfo.ConnectionType.String()),
			slog.String("device_name", p.ClientInfo.DeviceName), slog.String("atr", strings.ToUpper(hex.EncodeToString(p.ClientInfo.Atr))))
		if len(connTypes) != 0 && !slices.Contains(connTypes, p.ClientInfo.ConnectionType) {
			err = errors.New("client type mismatch")
			break
		}
	default:
		err = errors.New("unexpected response to request client info message")
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
	return p.ClientInfo.DeviceName
}

func (p *PCSC) ATR() ([]byte, error) {
	if p.ClientInfo.ConnectionType != subspacerelaypb.ConnectionType_CONNECTION_TYPE_PCSC {
		return nil, errors.New("atr not supported by connection type")
	}
	return p.ClientInfo.Atr, nil
}

func (p *PCSC) Reconnect() error {
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
		slog.ErrorContext(ctx, "Error parsing unsolicited message", ErrorAttrs(err))
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
