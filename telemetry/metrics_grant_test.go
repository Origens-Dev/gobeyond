package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type grantConnectionFixture struct {
	fixture *metricGrantFixture
	methods []string
	auth    []string
	corrupt bool
}

func (f *grantConnectionFixture) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unexpected stream")
}
func (f *grantConnectionFixture) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	f.methods = append(f.methods, method)
	md, _ := metadata.FromOutgoingContext(ctx)
	tokens := md.Get(tokenHeader)
	f.auth = append(f.auth, tokens[len(tokens)-1])
	req := args.(*structpb.Struct).AsMap()
	var g MetricGrant
	var err error
	switch method {
	case "/gobeyond.telemetry.v1.MetricProducerGrants/Acquire":
		g, err = f.fixture.Acquire(ctx, req["producer_instance_id"].(string))
	case "/gobeyond.telemetry.v1.MetricProducerGrants/Renew":
		g, err = f.fixture.Renew(ctx, req["producer_epoch"].(string))
	default:
		return errors.New("unexpected grant method")
	}
	if err != nil {
		return err
	}
	fields := map[string]any{"token": g.Token, "resource_id": g.ResourceID, "producer_id": g.ProducerID, "producer_epoch": g.ProducerEpoch, "epoch_started_at": float64(g.EpochStartedAt), "epoch_not_after": float64(g.EpochNotAfter), "retry_until": float64(g.RetryUntil)}
	if f.corrupt {
		fields["retry_until"] = float64(g.RetryUntil + 1)
	}
	response, _ := structpb.NewStruct(fields)
	proto.Merge(reply.(proto.Message), response)
	return nil
}
func TestMetricGrantGRPCWireAndOldEpochRenewalAuthentication(t *testing.T) {
	conn := &grantConnectionFixture{fixture: newGrantFixture()}
	provider, err := NewGRPCMetricGrantProvider(conn, "original-runtime-token")
	if err != nil {
		t.Fatal(err)
	}
	first, err := provider.Acquire(metadata.NewOutgoingContext(context.Background(), metadata.Pairs(tokenHeader, "untrusted-context-token")), strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Acquire(context.Background(), strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	if second.ProducerEpoch == first.ProducerEpoch {
		t.Fatal("reset reused epoch")
	}
	renewed, err := provider.Renew(context.Background(), first.ProducerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if conn.auth[0] != "original-runtime-token" || conn.auth[1] != first.Token || conn.auth[2] != first.Token || !renewed.sameIdentity(first) {
		t.Fatal("grant method authentication changed producer scope")
	}
	if _, err := provider.Renew(context.Background(), "unknown"); err == nil {
		t.Fatal("unknown epoch renewed")
	}
	conn.corrupt = true
	if _, err := provider.Acquire(context.Background(), strings.Repeat("c", 32)); err == nil {
		t.Fatal("response/token mismatch accepted")
	}
	for _, bad := range []string{"short", strings.Repeat("z", 32)} {
		if _, err := provider.Acquire(context.Background(), bad); err == nil {
			t.Fatal("malformed instance accepted")
		}
	}
}
func TestMetricGrantFixedDeadlineAndTokenBodyConsistency(t *testing.T) {
	fixture := newGrantFixture()
	g, _ := fixture.Acquire(context.Background(), strings.Repeat("a", 32))
	if err := g.validate(fixture.now); err != nil {
		t.Fatal(err)
	}
	g.RetryUntil++
	if err := g.validate(fixture.now); err == nil {
		t.Fatal("deadline changed without signed context")
	}
	g, _ = fixture.Acquire(context.Background(), strings.Repeat("a", 32))
	if err := g.validate(fixture.now.Add(48 * time.Hour)); err == nil {
		t.Fatal("expired grant accepted")
	}
}
