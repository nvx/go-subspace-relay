package subspacerelay

import (
	"context"
	"errors"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nvx/go-rfid"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"golang.org/x/sync/errgroup"
	"io"
	"log/slog"
	"slices"
	"sync"
)

type CardRelay[T io.Closer] struct {
	relay *SubspaceRelay

	relayInfoMutex sync.RWMutex
	relayInfo      *subspacerelaypb.RelayInfo

	emulateCancelMutex sync.Mutex
	emulateCancel      context.CancelFunc
	handler            CardRelayHandler[T]
	connectChan        chan *subspacerelaypb.Reconnect

	group    *errgroup.Group
	sequence uint32

	shortcutMutex       sync.Mutex
	lastShortcutStack   []emulationShortcut
	ephemeralShortcuts  []emulationShortcut
	persistentShortcuts []emulationShortcut
}

type CardRelayHandler[T io.Closer] interface {
	Setup(ctx context.Context, msg *subspacerelaypb.Reconnect) (T, error)
	Run(ctx context.Context, state T) error
}

func NewCardRelay[T io.Closer](relay *SubspaceRelay, relayInfo *subspacerelaypb.RelayInfo, handler CardRelayHandler[T]) *CardRelay[T] {
	return &CardRelay[T]{
		relay:       relay,
		relayInfo:   relayInfo,
		handler:     handler,
		connectChan: make(chan *subspacerelaypb.Reconnect, 1),
	}
}

func (h *CardRelay[T]) Run(ctx context.Context) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	for ctx.Err() == nil {
		err = h.relay.SendLog(context.WithoutCancel(ctx), &subspacerelaypb.Log{
			Message: "Waiting for signal to connect",
		})
		if err != nil {
			return
		}

		slog.InfoContext(ctx, "Waiting for signal to start")

		var msg *subspacerelaypb.Reconnect
		select {
		case msg = <-h.connectChan:
		case <-ctx.Done():
			return context.Cause(ctx)
		}

		h.setupAndRun(ctx, msg)
	}

	return context.Cause(ctx)
}

func (h *CardRelay[T]) setupAndRun(ctx context.Context, msg *subspacerelaypb.Reconnect) {
	err := h.relay.SendLog(context.WithoutCancel(ctx), &subspacerelaypb.Log{
		Message: "Setting up emulation",
	})
	if err != nil {
		slog.ErrorContext(ctx, "Error sending log", rfid.ErrorAttrs(err))
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	h.emulateCancelMutex.Lock()
	h.emulateCancel = cancel
	h.emulateCancelMutex.Unlock()

	h.group, ctx = errgroup.WithContext(ctx)
	h.group.SetLimit(CardShortcutLimit * 10)

	var state T
	state, err = h.handler.Setup(ctx, msg)
	if err != nil {
		_ = h.relay.SendLog(context.WithoutCancel(ctx), &subspacerelaypb.Log{
			Message: "Error during setup: " + err.Error(),
		})

		slog.ErrorContext(ctx, "Error during setup", rfid.ErrorAttrs(err))
		return
	}

	err = h.relay.SendLog(context.WithoutCancel(ctx), &subspacerelaypb.Log{
		Message: "Emulation setup complete",
	})
	if err != nil {
		slog.ErrorContext(ctx, "Error sending log", rfid.ErrorAttrs(err))
	}

	slog.InfoContext(ctx, "Emulation setup complete")

	err = h.run(ctx, state)
	if err != nil {
		_ = h.relay.SendLog(context.WithoutCancel(ctx), &subspacerelaypb.Log{
			Message: "Error during run: " + err.Error(),
		})

		slog.ErrorContext(ctx, "Error during run", rfid.ErrorAttrs(err))
	}

	err = h.relay.SendLog(context.WithoutCancel(ctx), &subspacerelaypb.Log{
		Message: "Emulation run complete",
	})
	if err != nil {
		slog.ErrorContext(ctx, "Error sending log", rfid.ErrorAttrs(err))
	}

	err = state.Close()
	if err != nil {
		_ = h.relay.SendLog(context.WithoutCancel(ctx), &subspacerelaypb.Log{
			Message: "Error during cleanup: " + err.Error(),
		})

		slog.ErrorContext(ctx, "Error during cleanup", rfid.ErrorAttrs(err))
	}
}

func (h *CardRelay[T]) run(ctx context.Context, state T) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	defer func() {
		if err != nil && errors.Is(err, context.Canceled) {
			err = nil
		}
	}()

	err = h.relay.SendLog(context.WithoutCancel(ctx), &subspacerelaypb.Log{
		Message: "Connected",
	})
	if err != nil {
		slog.ErrorContext(ctx, "Error sending log", rfid.ErrorAttrs(err))
	}

	slog.InfoContext(ctx, "Connected")

	h.group.Go(func() error {
		return h.handler.Run(ctx, state)
	})

	return h.group.Wait()
}

func (h *CardRelay[T]) Exchange(ctx context.Context, capdu []byte) (rapdu []byte, err error) {
	defer rfid.DeferWrap(ctx, &err)

	//fmt.Printf("RX: %X\n", capdu)
	//defer func() {
	//	if rapdu != nil {
	//		fmt.Printf("TX: %X\n", rapdu)
	//	} else {
	//		fmt.Printf("Error: %v\n", err)
	//	}
	//}()

	rapdu, err = h.tryHandleShortcut(ctx, capdu)
	if err != nil {
		return
	}
	if rapdu != nil {
		return rapdu, nil
	}

	responsePayload := &subspacerelaypb.Payload{
		Payload:     capdu,
		PayloadType: subspacerelaypb.PayloadType_PAYLOAD_TYPE_PCSC_CARD,
		Sequence:    h.sequence,
	}
	h.sequence++
	return h.relay.ExchangePayloadBytes(ctx, responsePayload)
}

func (h *CardRelay[T]) Connect(msg *subspacerelaypb.Reconnect) {
	// drain pending connects to ensure the data isn't stale
	select {
	case <-h.connectChan:
	default:
	}

	select {
	case h.connectChan <- msg:
	default:
	}
}

func (h *CardRelay[T]) Disconnect() {
	h.emulateCancelMutex.Lock()
	if h.emulateCancel != nil {
		h.emulateCancel()
		h.emulateCancel = nil
	}
	h.emulateCancelMutex.Unlock()

	h.shortcutMutex.Lock()
	h.persistentShortcuts = slices.DeleteFunc(h.persistentShortcuts, func(shortcut emulationShortcut) bool {
		return !shortcut.persistReconnect
	})
	h.ephemeralShortcuts = slices.DeleteFunc(h.ephemeralShortcuts, func(shortcut emulationShortcut) bool {
		return !shortcut.persistReconnect
	})
	h.shortcutMutex.Unlock()
}

func (h *CardRelay[T]) HandleMQTT(ctx context.Context, r *SubspaceRelay, p *paho.Publish) bool {
	// rpc responses are handled by the rpc router
	if len(p.Properties.CorrelationData) != 0 && p.Properties.ResponseTopic == "" {
		// response to an RPC, handled by the router
		//fmt.Println("RPC Reply")
		return false
	}

	req, err := r.Parse(ctx, p)
	if err != nil {
		slog.ErrorContext(ctx, "Error parsing request message", rfid.ErrorAttrs(err))
		return false
	}

	switch msg := req.Message.(type) {
	case *subspacerelaypb.Message_EmulationShortcut:
		h.shortcutMutex.Lock()
		h.addShortcutAlreadyHoldLock(msg.EmulationShortcut, p.Properties)
		if msg.EmulationShortcut.Persistent {
			sortShortcuts(h.persistentShortcuts)
		} else {
			sortShortcuts(h.ephemeralShortcuts)
		}
		h.shortcutMutex.Unlock()
	case *subspacerelaypb.Message_Disconnect:
		h.Disconnect()
	case *subspacerelaypb.Message_Reconnect:
		// cancel any existing emulation
		h.Disconnect()

		h.shortcutMutex.Lock()
		if msg.Reconnect.ForceFlushShortcuts {
			h.persistentShortcuts = h.persistentShortcuts[:0]
			h.ephemeralShortcuts = h.ephemeralShortcuts[:0]
		}
		for _, shortcutMsg := range msg.Reconnect.Shortcuts {
			h.addShortcutAlreadyHoldLock(shortcutMsg, p.Properties)
		}
		sortShortcuts(h.persistentShortcuts)
		sortShortcuts(h.ephemeralShortcuts)
		h.shortcutMutex.Unlock()

		// send signal to connect
		h.Connect(msg.Reconnect)
	case *subspacerelaypb.Message_RequestRelayInfo:
		h.relayInfoMutex.RLock()
		defer h.relayInfoMutex.RUnlock()
		err = r.SendReply(ctx, p.Properties, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_RelayInfo{
			RelayInfo: h.relayInfo,
		}})
	default:
		err = errors.New("unsupported message")
	}
	if err != nil {
		slog.ErrorContext(ctx, "Error handling request", rfid.ErrorAttrs(err))
		return false
	}
	return true
}

func (h *CardRelay[T]) addShortcutAlreadyHoldLock(msg *subspacerelaypb.EmulationShortcut, properties *paho.PublishProperties) {
	shortcut := mapShortcutCapdu(msg, properties)
	if msg.Persistent {
		h.persistentShortcuts = append(h.persistentShortcuts, shortcut)
	} else {
		h.ephemeralShortcuts = append(h.ephemeralShortcuts, shortcut)
	}
}

func (h *CardRelay[T]) WithRelayInfoLock(cb func(info *subspacerelaypb.RelayInfo)) {
	h.relayInfoMutex.Lock()
	defer h.relayInfoMutex.Unlock()
	cb(h.relayInfo)
}
