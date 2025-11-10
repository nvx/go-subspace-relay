package subspacerelay

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nvx/go-rfid"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"google.golang.org/protobuf/proto"
	"log/slog"
	"slices"
	"sync"
)

type Discovery struct {
	r            *SubspaceRelay
	cancel       context.CancelFunc
	plaintext    bool
	key          *ecdh.PrivateKey
	payloadTypes []subspacerelaypb.PayloadType
	m            sync.RWMutex
	ch           chan *subspacerelaypb.RelayDiscovery
}

type DiscoveryOption func(*Discovery)

func WithDiscoveryPrivateKey(key *ecdh.PrivateKey) DiscoveryOption {
	return func(d *Discovery) {
		d.key = key
	}
}

func WithDiscoveryPayloadType(payloadTypes ...subspacerelaypb.PayloadType) DiscoveryOption {
	return func(d *Discovery) {
		d.payloadTypes = payloadTypes
	}
}

func WithDiscoveryAllowPlaintext() DiscoveryOption {
	return func(d *Discovery) {
		d.plaintext = true
	}
}

func NewDiscovery(ctx context.Context, brokerURL string, opts ...DiscoveryOption) (_ *Discovery, err error) {
	defer rfid.DeferWrap(ctx, &err)

	ctx, cancel := context.WithCancel(ctx)

	// the relayID is a dummy value, but it never gets used
	sr, err := New(ctx, brokerURL, "discovery")
	if err != nil {
		cancel()
		return
	}

	d := &Discovery{
		r:      sr,
		cancel: cancel,
		ch:     make(chan *subspacerelaypb.RelayDiscovery, 1),
	}

	for _, o := range opts {
		o(d)
	}

	sr.RegisterHandler(HandlerFunc(d.handleMQTT))

	var publicKey []byte
	if d.key != nil {
		publicKey = d.key.PublicKey().Bytes()
	}

	err = sr.SendBroadcast(ctx, nil, &subspacerelaypb.Message{
		Message: &subspacerelaypb.Message_RequestRelayDiscovery{
			RequestRelayDiscovery: &subspacerelaypb.RequestRelayDiscovery{
				ControllerPublicKey: publicKey,
				PayloadTypes:        d.payloadTypes,
			},
		},
	})
	if err != nil {
		_ = d.Close()
		return
	}

	return d, nil
}

func (d *Discovery) Close() error {
	d.cancel()

	d.m.Lock()
	ch := d.ch
	d.ch = nil
	d.m.Unlock()

	if ch != nil {
		close(ch)
		return d.r.Close()
	}

	return nil
}

func (d *Discovery) Chan() <-chan *subspacerelaypb.RelayDiscovery {
	return d.ch
}

func (d *Discovery) handleMQTT(ctx context.Context, r *SubspaceRelay, p *paho.Publish) bool {
	req, err := r.Parse(ctx, p)
	if err != nil {
		slog.ErrorContext(ctx, "Error parsing request message", rfid.ErrorAttrs(err))
		return false
	}

	var relayDiscovery *subspacerelaypb.RelayDiscovery
	switch msg := req.Message.(type) {
	case *subspacerelaypb.Message_RelayDiscoveryEncrypted:
		if d.key == nil || !bytes.Equal(d.key.PublicKey().Bytes(), msg.RelayDiscoveryEncrypted.ControllerPublicKey) {
			return false
		}

		var pubKey *ecdh.PublicKey
		pubKey, err = ecdh.X25519().NewPublicKey(msg.RelayDiscoveryEncrypted.RelayPublicKey)
		if err != nil {
			slog.ErrorContext(ctx, "Error parsing relay public key from encrypted discovery message", rfid.ErrorAttrs(err))
			return false
		}

		var aead cipher.AEAD
		aead, _, err = GenerateECDHAESGCM(d.key, pubKey)
		if err != nil {
			slog.ErrorContext(ctx, "Error performing ECDH for encrypted discovery", rfid.ErrorAttrs(err))
			return false
		}

		payload := msg.RelayDiscoveryEncrypted.EncryptedRelayDiscovery
		payload, err = aead.Open(payload[:0], nil, payload, nil)
		if err != nil {
			slog.ErrorContext(ctx, "Error decrypting encrypted discovery", rfid.ErrorAttrs(err))
			return false
		}

		relayDiscovery = new(subspacerelaypb.RelayDiscovery)
		err = proto.Unmarshal(payload, relayDiscovery)
		if err != nil {
			slog.ErrorContext(ctx, "Error unmarshalling decrypted discovery message", rfid.ErrorAttrs(err))
			return false
		}
	case *subspacerelaypb.Message_RelayDiscoveryPlaintext:
		if !d.plaintext {
			return false
		}
		relayDiscovery = msg.RelayDiscoveryPlaintext
	default:
		return false
	}

	if relayDiscovery.RelayId == "" || relayDiscovery.RelayInfo == nil {
		slog.ErrorContext(ctx, "Got RelayDiscovery message with missing data")
		return false
	}

	if len(d.payloadTypes) > 0 &&
		!slices.ContainsFunc(relayDiscovery.RelayInfo.SupportedPayloadTypes, func(payloadType subspacerelaypb.PayloadType) bool {
			return slices.Contains(d.payloadTypes, payloadType)
		}) {
		// ignore unsupported payload type
		return false
	}

	d.m.RLock()
	select {
	case d.ch <- relayDiscovery:
	case <-ctx.Done():
	}
	d.m.RUnlock()

	return true
}

func (r *SubspaceRelay) HandleDiscoveryRequest(ctx context.Context, replyProperties *paho.PublishProperties, relayDiscovery *subspacerelaypb.RelayDiscovery, plaintextSupported bool, pubKeyWhitelist *ecdh.PublicKey, msg *subspacerelaypb.RequestRelayDiscovery) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	if !plaintextSupported && pubKeyWhitelist == nil {
		// discovery disabled
		return nil
	}

	if pubKeyWhitelist != nil && !bytes.Equal(pubKeyWhitelist.Bytes(), msg.ControllerPublicKey) {
		// secure discovery enabled but the pubkey doesn't match
		return nil
	}

	if len(msg.PayloadTypes) > 0 && !slices.Contains(msg.PayloadTypes, subspacerelaypb.PayloadType_PAYLOAD_TYPE_UNSPECIFIED) &&
		!slices.ContainsFunc(relayDiscovery.RelayInfo.SupportedPayloadTypes, func(payloadType subspacerelaypb.PayloadType) bool {
			return slices.Contains(msg.PayloadTypes, payloadType)
		}) {
		// controller is looking for a relay with a different payload type
		return nil
	}

	if pubKeyWhitelist == nil && len(msg.ControllerPublicKey) != 0 {
		pubKeyWhitelist, err = ecdh.X25519().NewPublicKey(msg.ControllerPublicKey)
		if err != nil {
			return
		}
	}

	return r.SendDiscoveryResponse(ctx, replyProperties, relayDiscovery, pubKeyWhitelist)
}

func (r *SubspaceRelay) SendDiscoveryResponse(ctx context.Context, replyProperties *paho.PublishProperties, relayDiscovery *subspacerelaypb.RelayDiscovery, pubKey *ecdh.PublicKey) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	if pubKey == nil {
		err = r.SendBroadcast(ctx, replyProperties, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_RelayDiscoveryPlaintext{
			RelayDiscoveryPlaintext: relayDiscovery,
		}})
		return
	}

	aead, relayPubKey, err := GenerateECDHAESGCM(nil, pubKey)
	if err != nil {
		return
	}

	b, err := proto.Marshal(relayDiscovery)
	if err != nil {
		return
	}

	err = r.SendBroadcast(ctx, replyProperties, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_RelayDiscoveryEncrypted{
		RelayDiscoveryEncrypted: &subspacerelaypb.RelayDiscoveryEncrypted{
			ControllerPublicKey:     pubKey.Bytes(),
			RelayPublicKey:          relayPubKey.Bytes(),
			EncryptedRelayDiscovery: aead.Seal(b[:0], nil, b, nil),
		},
	}})
	if err != nil {
		return
	}

	return nil
}
