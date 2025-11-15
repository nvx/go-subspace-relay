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
	"sync"
)

const CardShortcutLimit = 50

type CardController struct {
	Relay       *SubspaceRelay
	ctxCancel   context.CancelFunc
	RelayInfo   *subspacerelaypb.RelayInfo
	handler     CardControllerHandler
	Resequencer Resequencer
	closeOnce   func() error
}

type CardControllerHandler interface {
	rfid.Exchanger
	Disconnect(context.Context)
}

func NewCardController(ctx context.Context, serverURL, relayID string, connTypes []subspacerelaypb.ConnectionType, handler CardControllerHandler) (_ *CardController, err error) {
	defer rfid.DeferWrap(ctx, &err)

	relay, err := New(ctx, serverURL, relayID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(ctx)

	c := &CardController{
		Relay:       relay,
		ctxCancel:   cancel,
		handler:     handler,
		Resequencer: NoopResequencer,
	}
	c.closeOnce = sync.OnceValue(c.close)

	relay.RegisterHandler(c)

	slog.InfoContext(ctx, "Requesting relay info")
	msg, err := relay.Exchange(ctx, &subspacerelaypb.Message{
		Message: &subspacerelaypb.Message_RequestRelayInfo{RequestRelayInfo: &emptypb.Empty{}},
	})
	if err != nil {
		_ = c.Close()
		return
	}

	switch msg := msg.Message.(type) {
	case *subspacerelaypb.Message_RelayInfo:
		c.RelayInfo = msg.RelayInfo
		slog.InfoContext(ctx, "Got relay info", RelayInfoAttrs(c.RelayInfo))
		if !slices.Contains(connTypes, c.RelayInfo.ConnectionType) ||
			!slices.Contains(c.RelayInfo.SupportedPayloadTypes, subspacerelaypb.PayloadType_PAYLOAD_TYPE_PCSC_CARD) {
			err = errors.New("relay type mismatch")
			break
		}
	default:
		err = errors.New("unexpected response to request relay info message")
	}
	if err != nil {
		_ = c.Close()
		return
	}

	return c, nil
}

// AddGeneralShortcut adds a non-rpc based general shortcut
// This is mostly useful for persistent shortcuts
func (c *CardController) AddGeneralShortcut(ctx context.Context, msg *subspacerelaypb.EmulationShortcut) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	if !isValidEmulationShortcut(msg) {
		err = errors.New("invalid EmulationShortcut")
		return
	}

	err = c.Relay.SendUnsolicited(ctx, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_EmulationShortcut{EmulationShortcut: msg}})
	if err != nil {
		return
	}

	return nil
}

func (c *CardController) HandleMQTT(ctx context.Context, r *SubspaceRelay, p *paho.Publish) bool {
	req, err := r.Parse(ctx, p)
	if err != nil {
		slog.ErrorContext(ctx, "Error parsing unsolicited message", rfid.ErrorAttrs(err))
		return false
	}

	switch msg := req.Message.(type) {
	case *subspacerelaypb.Message_Payload:
		capdu := msg.Payload.Payload
		if p.Properties.ResponseTopic == "" {
			// non-rpc shortcut reply
			if len(p.Properties.CorrelationData) != 0 {
				slog.WarnContext(ctx, "Lost RPC", rfid.LogHex("correlation", p.Properties.CorrelationData), rfid.LogHex("capdu", capdu))
			}
			err = c.Resequencer(ctx, msg.Payload.Sequence, func(ctx context.Context) (err error) {
				defer rfid.DeferWrap(ctx, &err)
				_, err = c.handler.Exchange(ctx, capdu)
				return err
			})
		} else {
			// non-shortcut packets
			slog.DebugContext(ctx, "Non-shortcut cAPDU", rfid.LogHex("capdu", capdu))
			err = r.HandlePayload(ctx, p.Properties, msg.Payload, func(ctx context.Context, _ *subspacerelaypb.Payload) (ret []byte, err error) {
				err = c.Resequencer(ctx, msg.Payload.Sequence, func(ctx context.Context) (err error) {
					ret, err = c.handler.Exchange(ctx, capdu)
					if err != nil {
						// log error and return 6F00 to prevent emulation from hanging
						slog.ErrorContext(ctx, "Error handling non-shortcut packet payload", rfid.ErrorAttrs(err))
						ret = []byte{0x6F, 0x00}
					}
					return nil
				})
				return ret, err
			}, subspacerelaypb.PayloadType_PAYLOAD_TYPE_PCSC_CARD)
		}
	case *subspacerelaypb.Message_Log:
		slog.InfoContext(ctx, "Remote Log: "+msg.Log.Message)
	case *subspacerelaypb.Message_Disconnect:
		slog.InfoContext(ctx, "Remote disconnected")
		c.handler.Disconnect(ctx)
	default:
		return false
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.ErrorContext(ctx, "Error handling unsolicited message", rfid.ErrorAttrs(err))
		return false
	}
	return true
}

func (c *CardController) Reconnect(ctx context.Context, msg *subspacerelaypb.Reconnect) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	return c.Relay.SendUnsolicited(ctx, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Reconnect{Reconnect: msg}})
}

func (c *CardController) Disconnect(ctx context.Context) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	return c.Relay.SendUnsolicited(ctx, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Disconnect{Disconnect: &emptypb.Empty{}}})
}

func (c *CardController) Close() (err error) {
	return c.closeOnce()
}

func (c *CardController) close() (err error) {
	ctx := context.Background()
	err = c.Disconnect(ctx)
	if err != nil {
		c.ctxCancel()
		_ = c.Relay.Close()
		return
	}

	c.ctxCancel()

	return c.Relay.Close()
}
