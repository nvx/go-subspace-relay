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
	"log/slog"
	"net/url"
	"strings"
	"sync"
)

const (
	topicFormatToRelay   = "subspace/endpoint/%s/to-relay"
	topicFormatFromRelay = "subspace/endpoint/%s/from-relay"
)

type Handler interface {
	HandleMQTT(ctx context.Context, r *SubspaceRelay, p *paho.Publish) bool
}

type SubspaceRelay struct {
	RelayID     string
	isRelaySide bool

	conn *autopaho.ConnectionManager

	readTopic  string
	writeTopic string

	rpcPending map[string]chan<- *paho.Publish

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
		slog.ErrorContext(ctx, msg, ErrorAttrs(err))
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
		}})
		if err != nil {
			slog.ErrorContext(ctx, "Failed to subscribe", ErrorAttrs(err))
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
	defer DeferWrap(&err)

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
		// ensure the other end has a unique client id too
		clientSuffix = "-" + uniqueID
	}

	bID, err := pbkdf2.Key(sha256.New, relayID, []byte("mqtt-id"), 20, 16)
	if err != nil {
		panic(err)
	}
	mqttClientID := hex.EncodeToString(bID) // this encodes to lowercase

	r := &SubspaceRelay{
		isRelaySide: isRelaySide,
		RelayID:     relayID,

		rpcPending: make(map[string]chan<- *paho.Publish),
		crypto:     newCrypto(relayID),
	}

	if isRelaySide {
		r.readTopic = fmt.Sprintf(topicFormatToRelay, mqttClientID)
		r.writeTopic = fmt.Sprintf(topicFormatFromRelay, mqttClientID)
	} else {
		r.readTopic = fmt.Sprintf(topicFormatFromRelay, mqttClientID)
		r.writeTopic = fmt.Sprintf(topicFormatToRelay, mqttClientID)
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
