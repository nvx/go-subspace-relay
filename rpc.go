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

func (r *SubspaceRelay) Exchange(ctx context.Context, message *subspacerelaypb.Message) (_ *subspacerelaypb.Message, err error) {
	defer rfid.DeferWrap(ctx, &err)

	messageBytes, err := proto.Marshal(message)
	if err != nil {
		return
	}

	messageBytes = r.crypto.encrypt(messageBytes)

	ch, id := r.addPendingRPC()

	pb := &paho.Publish{
		QoS:     2,
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
		r.getPendingRPC(id)
		return
	}

	select {
	case <-ctx.Done():
		r.getPendingRPC(id)
		err = context.Cause(ctx)
		return
	case res := <-ch:
		return r.Parse(ctx, res)
	}
}

func (r *SubspaceRelay) addPendingRPC() (<-chan *paho.Publish, []byte) {
	r.lock.Lock()
	defer r.lock.Unlock()

	ch := make(chan *paho.Publish, 1)

	id := make([]byte, 16)
	_, err := io.ReadFull(rand.Reader, id)
	if err != nil {
		panic(err)
	}

	r.rpcPending[string(id)] = ch

	return ch, id
}

func (r *SubspaceRelay) getPendingRPC(id []byte) chan<- *paho.Publish {
	r.lock.Lock()
	defer r.lock.Unlock()

	s := string(id)

	ch := r.rpcPending[s]
	delete(r.rpcPending, s)

	return ch
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
