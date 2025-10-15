package subspacerelay

import (
	"context"
	"crypto/rand"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nvx/go-rfid"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"google.golang.org/protobuf/proto"
	"io"
)

type pendingRPC struct {
	persistent bool
	ch         chan<- *paho.Publish
}

func (r *SubspaceRelay) Exchange(ctx context.Context, message *subspacerelaypb.Message) (_ *subspacerelaypb.Message, err error) {
	ch, id, err := r.exchange(ctx, message, false)
	if err != nil {
		return
	}

	select {
	case <-ctx.Done():
		r.deletePendingRPC(id)
		err = context.Cause(ctx)
		return
	case res := <-ch:
		return r.Parse(ctx, res)
	}
}

func (r *SubspaceRelay) ExchangeMulti(ctx context.Context, message *subspacerelaypb.Message, cb func(context.Context, *subspacerelaypb.Message) (final bool, err error)) (err error) {
	ch, id, err := r.exchange(ctx, message, true)
	if err != nil {
		return
	}
	defer r.deletePendingRPC(id)

	for {
		select {
		case <-ctx.Done():
			err = context.Cause(ctx)
			return
		case res := <-ch:
			var m *subspacerelaypb.Message
			m, err = r.Parse(ctx, res)
			if err != nil {
				return
			}

			var final bool
			final, err = cb(ctx, m)
			if err != nil || final {
				return
			}
		}
	}
}

func (r *SubspaceRelay) exchange(ctx context.Context, message *subspacerelaypb.Message, persistent bool) (_ <-chan *paho.Publish, _ []byte, err error) {
	defer rfid.DeferWrap(ctx, &err)

	messageBytes, err := proto.Marshal(message)
	if err != nil {
		return
	}

	messageBytes = r.crypto.encrypt(messageBytes)

	ch, id := r.addPendingRPC(persistent)

	pb := &paho.Publish{
		QoS:     qosExactlyOnce,
		Topic:   r.writeTopic,
		Payload: messageBytes,
		Properties: &paho.PublishProperties{
			ContentType:     ContentTypeProto,
			CorrelationData: id,
			ResponseTopic:   r.readTopic,
		},
		Retain: false,
	}

	_, err = r.conn.Publish(ctx, pb)
	if err != nil {
		r.deletePendingRPC(id)
		return
	}

	return ch, id, nil
}

func (r *SubspaceRelay) addPendingRPC(persistent bool) (<-chan *paho.Publish, []byte) {
	r.lock.Lock()
	defer r.lock.Unlock()

	l := 1
	if persistent {
		l = 10
	}
	ch := make(chan *paho.Publish, l)

	id := make([]byte, 16)
	_, err := io.ReadFull(rand.Reader, id)
	if err != nil {
		panic(err)
	}

	r.rpcPending[string(id)] = pendingRPC{
		persistent: persistent,
		ch:         ch,
	}

	return ch, id
}

func (r *SubspaceRelay) deletePendingRPC(id []byte) {
	s := string(id)

	r.lock.Lock()
	defer r.lock.Unlock()

	delete(r.rpcPending, s)
}

func (r *SubspaceRelay) getPendingRPC(id []byte) chan<- *paho.Publish {
	s := string(id)

	r.lock.Lock()
	defer r.lock.Unlock()

	pending := r.rpcPending[s]
	if !pending.persistent {
		delete(r.rpcPending, s)
	}

	return pending.ch
}

func (r *SubspaceRelay) rpcHandleReply(pb *paho.Publish) bool {
	if pb.Properties == nil || pb.Properties.CorrelationData == nil {
		return false
	}

	ch := r.getPendingRPC(pb.Properties.CorrelationData)
	if ch == nil {
		return false
	}

	select {
	case ch <- pb:
		return true
	default:
		return false
	}
}
