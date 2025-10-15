package subspacerelay

import (
	"bytes"
	"context"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nvx/go-apdu"
	"github.com/nvx/go-rfid"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"log/slog"
	"slices"
)

type emulationShortcut struct {
	capduHeader      [][]byte
	capduData        [][]byte
	rapdu            []byte
	sendCapdu        bool
	properties       *paho.PublishProperties
	persistReconnect bool

	usedEphemeral      map[int]struct{}
	ephemeralChildren  []emulationShortcut
	persistentChildren []emulationShortcut
}

func mapShortcutCapdu(msg *subspacerelaypb.EmulationShortcut, properties *paho.PublishProperties) emulationShortcut {
	s := emulationShortcut{
		capduHeader:      msg.CapduHeader,
		capduData:        msg.CapduData,
		rapdu:            msg.Rapdu,
		sendCapdu:        msg.SendCapdu,
		properties:       properties,
		persistReconnect: msg.PersistReconnect,
		usedEphemeral:    make(map[int]struct{}),
	}

	for _, next := range msg.ChainedNext {
		c := mapShortcutCapdu(next, properties)
		if next.Persistent {
			s.persistentChildren = append(s.persistentChildren, c)
		} else {
			s.ephemeralChildren = append(s.ephemeralChildren, c)
		}
	}

	return s
}

func (s emulationShortcut) capduMatch(ctx context.Context, capdu apdu.Capdu) bool {
	if len(s.capduHeader) != 0 {
		var found bool
		for _, h := range s.capduHeader {
			if len(h) != 4 {
				slog.ErrorContext(ctx, "got EmulationShortcut with invalid capdu_header bytes length")
				continue
			}
			if h[0] == capdu.CLA && h[1] == capdu.INS && h[2] == capdu.P1 && h[3] == capdu.P2 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if len(s.capduData) == 0 {
		return true
	}

	for _, d := range s.capduData {
		if bytes.Equal(d, capdu.Data) {
			return true
		}
	}

	return false
}

func (h *CardRelay[T]) tryHandleShortcut(ctx context.Context, capdu []byte) (rapdu []byte, err error) {
	defer rfid.DeferWrap(ctx, &err)

	parsedCapdu, err := apdu.ParseCapdu(capdu)
	if err != nil {
		// handle as non-shortcut
		err = nil
		return
	}

	for {
		lastShortcut := h.lastShortcut()
		if lastShortcut == nil {
			break
		}

		for i, shortcut := range lastShortcut.ephemeralChildren {
			if _, exists := lastShortcut.usedEphemeral[i]; exists {
				continue
			}

			if shortcut.capduMatch(ctx, parsedCapdu) {
				lastShortcut.usedEphemeral[i] = struct{}{}

				return h.handleShortcut(ctx, capdu, shortcut)
			}
		}

		for _, shortcut := range lastShortcut.persistentChildren {
			if shortcut.capduMatch(ctx, parsedCapdu) {
				return h.handleShortcut(ctx, capdu, shortcut)
			}
		}

		h.popLastShortcut()
	}

	h.shortcutMutex.Lock()
	defer h.shortcutMutex.Unlock()

	for i, shortcut := range h.ephemeralShortcuts {
		if shortcut.capduMatch(ctx, parsedCapdu) {
			h.ephemeralShortcuts = slices.Delete(h.ephemeralShortcuts, i, i+1)

			return h.handleShortcut(ctx, capdu, shortcut)
		}
	}

	for _, shortcut := range h.persistentShortcuts {
		if shortcut.capduMatch(ctx, parsedCapdu) {
			return h.handleShortcut(ctx, capdu, shortcut)
		}
	}

	return nil, nil
}

func (h *CardRelay[T]) handleShortcut(ctx context.Context, capdu []byte, shortcut emulationShortcut) (rapdu []byte, err error) {
	defer rfid.DeferWrap(ctx, &err)

	clear(shortcut.usedEphemeral)
	h.lastShortcutStack = append(h.lastShortcutStack, shortcut)

	if shortcut.sendCapdu {
		responsePayload := &subspacerelaypb.Payload{
			Payload:     capdu,
			PayloadType: subspacerelaypb.PayloadType_PAYLOAD_TYPE_PCSC_CARD,
			Sequence:    h.sequence,
		}
		h.sequence++

		h.group.Go(func() error {
			return h.relay.SendReply(ctx, shortcut.properties, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Payload{Payload: responsePayload}})
		})
	}

	return shortcut.rapdu, nil
}

func (h *CardRelay[T]) lastShortcut() *emulationShortcut {
	if len(h.lastShortcutStack) == 0 {
		return nil
	}
	return &h.lastShortcutStack[len(h.lastShortcutStack)-1]
}

func (h *CardRelay[T]) popLastShortcut() {
	if len(h.lastShortcutStack) > 0 {
		h.lastShortcutStack = h.lastShortcutStack[:len(h.lastShortcutStack)-1]
	}
}

func isValidEmulationShortcut(msg *subspacerelaypb.EmulationShortcut) bool {
	if msg == nil || slices.ContainsFunc(msg.CapduHeader, func(b []byte) bool {
		return len(b) != 4
	}) {
		return false
	}

	for _, child := range msg.ChainedNext {
		if !isValidEmulationShortcut(child) {
			return false
		}
	}

	return true
}

func sortShortcuts(l []emulationShortcut) {
	slices.SortStableFunc(l, func(a, b emulationShortcut) int {
		if (len(a.capduHeader) == 0) != (len(b.capduHeader) == 0) {
			if len(a.capduHeader) == 0 {
				return 1
			}
			return -1
		}
		if (len(a.capduData) == 0) != (len(b.capduData) == 0) {
			if len(a.capduData) == 0 {
				return 1
			}
			return -1
		}
		return 0
	})
}
