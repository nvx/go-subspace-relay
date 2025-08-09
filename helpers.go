package subspacerelay

import (
	"context"
	"errors"
	"github.com/eclipse/paho.golang/paho"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"google.golang.org/protobuf/proto"
)

func (r *SubspaceRelay) Parse(ctx context.Context, p *paho.Publish) (_ *subspacerelaypb.Message, err error) {
	defer DeferWrap(&err)

	if p.Properties == nil || p.Properties.ContentType != "application/proto" {
		err = errors.New("unexpected content-type")
		return
	}

	m, err := r.crypto.decrypt(p.Payload)
	if err != nil {
		return
	}

	msg := new(subspacerelaypb.Message)
	err = proto.Unmarshal(m, msg)
	if err != nil {
		return
	}

	return msg, nil
}

func (r *SubspaceRelay) ExchangePayloadBytes(ctx context.Context, payload *subspacerelaypb.Payload) (_ []byte, err error) {
	defer DeferWrap(&err)

	p, err := r.ExchangePayload(ctx, payload)
	if err != nil {
		return
	}

	return p.Payload, nil
}

func (r *SubspaceRelay) ExchangePayload(ctx context.Context, payload *subspacerelaypb.Payload) (_ *subspacerelaypb.Payload, err error) {
	defer DeferWrap(&err)

	reply, err := r.Exchange(ctx, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Payload{Payload: payload}})
	if err != nil {
		return
	}

	switch msg := reply.Message.(type) {
	case *subspacerelaypb.Message_Payload:
		if msg.Payload.PayloadType != payload.PayloadType {
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
	defer DeferWrap(&err)

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

	r.sequence++
	responsePayload := &subspacerelaypb.Payload{
		Payload:     response,
		PayloadType: payload.PayloadType,
		Sequence:    r.sequence,
	}

	err = r.SendReply(ctx, properties, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Payload{Payload: responsePayload}})
	if err != nil {
		return
	}

	return nil
}

func (r *SubspaceRelay) SendReply(ctx context.Context, properties *paho.PublishProperties, message *subspacerelaypb.Message) (err error) {
	defer DeferWrap(&err)

	if len(properties.CorrelationData) == 0 || properties.ResponseTopic == "" {
		err = errors.New("missing response topic")
		return
	}

	reply, err := proto.Marshal(message)
	if err != nil {
		return
	}

	reply = r.crypto.encrypt(reply)

	_, err = r.conn.Publish(ctx, &paho.Publish{
		QoS:     2,
		Topic:   properties.ResponseTopic,
		Payload: reply,
		Properties: &paho.PublishProperties{
			ContentType:     "application/proto",
			CorrelationData: properties.CorrelationData,
		},
	})
	if err != nil {
		return
	}

	return nil
}

func (r *SubspaceRelay) SendUnsolicited(ctx context.Context, message *subspacerelaypb.Message) (err error) {
	defer DeferWrap(&err)

	reply, err := proto.Marshal(message)
	if err != nil {
		return
	}

	reply = r.crypto.encrypt(reply)

	_, err = r.conn.Publish(ctx, &paho.Publish{
		QoS:     2,
		Topic:   r.writeTopic,
		Payload: reply,
		Properties: &paho.PublishProperties{
			ContentType: ContentTypeProto,
		},
	})
	if err != nil {
		return
	}

	return nil
}

func (r *SubspaceRelay) SendLog(ctx context.Context, log *subspacerelaypb.Log) (err error) {
	defer DeferWrap(&err)

	return r.SendUnsolicited(ctx, &subspacerelaypb.Message{Message: &subspacerelaypb.Message_Log{Log: log}})
}
