package subspacerelay

import (
	"context"
	"errors"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nvx/go-rfid"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"google.golang.org/protobuf/proto"
)

func (r *SubspaceRelay) Parse(ctx context.Context, p *paho.Publish) (_ *subspacerelaypb.Message, err error) {
	defer rfid.DeferWrap(ctx, &err)

	if p.Properties == nil || p.Properties.ContentType != ContentTypeProto {
		err = errors.New("unexpected content-type")
		return
	}

	m := p.Payload

	if p.Topic != r.readBroadcast {
		m, err = r.crypto.decrypt(p.Payload)
		if err != nil {
			return
		}
	}

	msg := new(subspacerelaypb.Message)
	err = proto.Unmarshal(m, msg)
	if err != nil {
		return
	}

	if p.Topic == r.readBroadcast {
		switch msg.Message.(type) {
		case *subspacerelaypb.Message_RequestRelayDiscovery,
			*subspacerelaypb.Message_RelayDiscoveryPlaintext,
			*subspacerelaypb.Message_RelayDiscoveryEncrypted:
		default:
			err = errors.New("got non-discovery message on broadcast topic")
			return
		}
	}

	return msg, nil
}

func (r *SubspaceRelay) ExchangePayloadBytes(ctx context.Context, payload *subspacerelaypb.Payload) (_ []byte, err error) {
	defer rfid.DeferWrap(ctx, &err)

	p, err := r.ExchangePayload(ctx, payload)
	if err != nil {
		return
	}

	return p.Payload, nil
}

func (r *SubspaceRelay) ExchangePayload(ctx context.Context, payload *subspacerelaypb.Payload) (_ *subspacerelaypb.Payload, err error) {
	defer rfid.DeferWrap(ctx, &err)

	return r.ExchangeMessageForPayload(ctx, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Payload{Payload: payload}}, payload.PayloadType)
}

func (r *SubspaceRelay) ExchangeMessageForPayload(ctx context.Context, message *subspacerelaypb.Message, expectedPayloadType subspacerelaypb.PayloadType) (_ *subspacerelaypb.Payload, err error) {
	defer rfid.DeferWrap(ctx, &err)

	reply, err := r.Exchange(ctx, message)
	if err != nil {
		return
	}

	switch msg := reply.Message.(type) {
	case *subspacerelaypb.Message_Payload:
		if msg.Payload.PayloadType != expectedPayloadType && expectedPayloadType != subspacerelaypb.PayloadType_PAYLOAD_TYPE_UNSPECIFIED {
			err = errors.New("unexpected payload type")
			return
		}
		return msg.Payload, nil
	default:
		err = errors.New("unexpected reply message type")
		return
	}
}

func (r *SubspaceRelay) HandlePayload(ctx context.Context, properties *paho.PublishProperties, payload *subspacerelaypb.Payload,
	handler func(context.Context, *subspacerelaypb.Payload) ([]byte, error), supportedPayloadTypes ...subspacerelaypb.PayloadType) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	if len(supportedPayloadTypes) > 0 {
		var found bool
		for _, t := range supportedPayloadTypes {
			if payload.PayloadType == t {
				found = true
				break
			}
		}
		if !found {
			err = errors.New("unsupported payload type: " + payload.PayloadType.String())
			return
		}
	}

	if properties.ResponseTopic == "" {
		err = errors.New("missing response topic")
		return
	}

	response, err := handler(ctx, payload)
	if err != nil {
		return
	}

	responsePayload := &subspacerelaypb.Payload{
		Payload:     response,
		PayloadType: payload.PayloadType,
		Sequence:    r.sequence,
	}
	r.sequence++

	err = r.SendReply(ctx, properties, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Payload{Payload: responsePayload}})
	if err != nil {
		return
	}

	return nil
}

func (r *SubspaceRelay) SendReply(ctx context.Context, properties *paho.PublishProperties, message *subspacerelaypb.Message) (err error) {
	return r.send(ctx, r.writeTopic, properties, message, r.crypto)
}

func (r *SubspaceRelay) SendBroadcast(ctx context.Context, replyProperties *paho.PublishProperties, message *subspacerelaypb.Message) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	return r.send(ctx, r.writeBroadcast, replyProperties, message, nil)
}

func (r *SubspaceRelay) send(ctx context.Context, topic string, replyProperties *paho.PublishProperties, message *subspacerelaypb.Message, encrypter messageEncrypter) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	var correlationData []byte
	if replyProperties != nil {
		correlationData = replyProperties.CorrelationData
		if replyProperties.ResponseTopic != "" {
			topic = replyProperties.ResponseTopic
		}
	}

	reply, err := proto.Marshal(message)
	if err != nil {
		return
	}

	if encrypter != nil {
		reply = encrypter.encrypt(reply)
	}

	_, err = r.conn.Publish(ctx, &paho.Publish{
		QoS:     qosExactlyOnce,
		Topic:   topic,
		Payload: reply,
		Properties: &paho.PublishProperties{
			ContentType:     ContentTypeProto,
			CorrelationData: correlationData,
		},
	})
	if err != nil {
		return
	}

	return nil
}

func (r *SubspaceRelay) SendUnsolicited(ctx context.Context, message *subspacerelaypb.Message) (err error) {
	return r.SendReply(ctx, nil, message)
}

func (r *SubspaceRelay) SendLog(ctx context.Context, log *subspacerelaypb.Log) (err error) {
	defer rfid.DeferWrap(ctx, &err)

	return r.SendUnsolicited(ctx, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Log{Log: log}})
}
