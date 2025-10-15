package subspacerelay

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/google/uuid"
	"github.com/nvx/go-rfid"
	"log/slog"
	"net/url"
	"strings"
	"sync"
)

const (
	topicFormatToRelay           = "subspace/endpoint/%s/to-relay"
	topicFormatFromRelay         = "subspace/endpoint/%s/from-relay"
	topicBroadcastToRelay        = "subspace/broadcast/to-relay"
	topicBroadcastFromRelay      = "subspace/broadcast/from-relay"
	qosAtMostOnce           byte = 0
	qosAtLeastOnce          byte = 1
	qosExactlyOnce          byte = 2
)

type Handler interface {
	HandleMQTT(ctx context.Context, r *SubspaceRelay, p *paho.Publish) bool
}

type HandlerFunc func(ctx context.Context, r *SubspaceRelay, p *paho.Publish) bool

func (f HandlerFunc) HandleMQTT(ctx context.Context, r *SubspaceRelay, p *paho.Publish) bool {
	return f(ctx, r, p)
}

var _ Handler = HandlerFunc(nil)

type SubspaceRelay struct {
	RelayID     string
	isRelaySide bool

	conn *autopaho.ConnectionManager

	readTopic      string
	writeTopic     string
	readBroadcast  string
	writeBroadcast string

	rpcPending map[string]pendingRPC

	lock     sync.Mutex
	handlers []Handler
	sequence uint32
	crypto   crypto
}

func (r *SubspaceRelay) onPublishReceived(ctx context.Context) func(p paho.PublishReceived) (bool, error) {
	return func(p paho.PublishReceived) (bool, error) {
		if r.rpcHandleReply(p.Packet) {
			return true, nil
		}

		for _, h := range r.handlers {
			if h.HandleMQTT(ctx, r, p.Packet) {
				return true, nil
			}
		}

		return false, nil
	}
}

func (r *SubspaceRelay) onError(ctx context.Context, msg string) func(err error) {
	return func(err error) {
		slog.ErrorContext(ctx, msg, rfid.ErrorAttrs(err))
	}
}

func (r *SubspaceRelay) onServerDisconnect(ctx context.Context) func(*paho.Disconnect) {
	return func(d *paho.Disconnect) {
		attrs := []slog.Attr{slog.Int("code", int(d.ReasonCode))}
		if d.Properties != nil {
			attrs = append(attrs, slog.String("string", d.Properties.ReasonString))
		}
		slog.WarnContext(ctx, "Server requested disconnect", slog.Attr{Key: "reason", Value: slog.GroupValue(attrs...)})
	}
}

func (r *SubspaceRelay) onConnectionUp(ctx context.Context) func(cm *autopaho.ConnectionManager, connAck *paho.Connack) {
	return func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
		_, err := cm.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{
			{
				Topic:   r.readTopic,
				NoLocal: true,
			},
			{
				Topic:   r.readBroadcast,
				NoLocal: true,
			},
		}})
		if err != nil {
			slog.ErrorContext(ctx, "Failed to subscribe", rfid.ErrorAttrs(err))
		}
	}
}

func (r *SubspaceRelay) Close() error {
	return r.conn.Disconnect(context.Background())
}

func (r *SubspaceRelay) RegisterHandler(h Handler) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.handlers = append(r.handlers, h)
}

// New returns a new SubspaceRelay instance
// relayID must be "" if this end is the relay, otherwise if this is the controlling end provide the relayID of the
// other side which is randomly generated on startup and can be read from SubspaceRelay.RelayID
func New(ctx context.Context, brokerURL, relayID string) (_ *SubspaceRelay, err error) {
	defer rfid.DeferWrap(ctx, &err)

	u, err := url.Parse(brokerURL)
	if err != nil {
		return
	}

	var newUUID uuid.UUID
	newUUID, err = uuid.NewV7()
	if err != nil {
		return
	}

	uniqueID := strings.ReplaceAll(newUUID.String(), "-", "")

	var clientSuffix string
	var isRelaySide bool
	if relayID == "" {
		relayID = uniqueID
		isRelaySide = true
	} else {
		// ensure the controller side has a unique client id too
		clientSuffix = "-controller-" + uniqueID
	}

	bID, err := pbkdf2.Key(sha256.New, relayID, []byte("mqtt-id"), 20, 16)
	if err != nil {
		panic(err)
	}
	mqttClientID := hex.EncodeToString(bID) // this encodes to lowercase

	r := &SubspaceRelay{
		isRelaySide: isRelaySide,
		RelayID:     relayID,

		rpcPending: make(map[string]pendingRPC),
		crypto:     newCrypto(relayID),
	}

	if isRelaySide {
		r.readTopic = fmt.Sprintf(topicFormatToRelay, mqttClientID)
		r.writeTopic = fmt.Sprintf(topicFormatFromRelay, mqttClientID)
		r.readBroadcast = topicBroadcastToRelay
		r.writeBroadcast = topicBroadcastFromRelay
	} else {
		r.readTopic = fmt.Sprintf(topicFormatFromRelay, mqttClientID)
		r.writeTopic = fmt.Sprintf(topicFormatToRelay, mqttClientID)
		r.readBroadcast = topicBroadcastFromRelay
		r.writeBroadcast = topicBroadcastToRelay
	}

	userInfo := u.User
	u.User = nil

	password, _ := userInfo.Password()
	var passwordBytes []byte
	if password != "" {
		passwordBytes = []byte(password)
	}

	r.conn, err = autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{u},
		ConnectUsername:               userInfo.Username(),
		ConnectPassword:               passwordBytes,
		CleanStartOnInitialConnection: true,
		KeepAlive:                     60,
		SessionExpiryInterval:         60,
		ClientConfig: paho.ClientConfig{
			ClientID:           mqttClientID + clientSuffix,
			OnPublishReceived:  []func(paho.PublishReceived) (bool, error){r.onPublishReceived(ctx)},
			OnClientError:      r.onError(ctx, "MQTT Client Error"),
			OnServerDisconnect: r.onServerDisconnect(ctx),
		},
		OnConnectionUp: r.onConnectionUp(ctx),
		OnConnectError: r.onError(ctx, "MQTT Connection Error"),
	})
	if err != nil {
		return
	}

	err = r.conn.AwaitConnection(ctx)
	if err != nil {
		return
	}

	return r, nil
}
