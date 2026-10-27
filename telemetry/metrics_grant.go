package telemetry

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"
)

const MetricPointIDAttribute = "gobeyond.metric.point_id"

// MetricGrant contains platform-issued capability data. The SDK never signs or
// supplies producer identity to the receiver; its token is verified by the host.
type MetricGrant struct {
	Token          string `json:"-"`
	ResourceID     string `json:"resource_id"`
	ProducerID     string `json:"producer_id"`
	ProducerEpoch  string `json:"producer_epoch"`
	EpochStartedAt int64  `json:"epoch_started_at"`
	EpochNotAfter  int64  `json:"epoch_not_after"`
	RetryUntil     int64  `json:"retry_until"`
}

// MetricGrantProvider acquires a durable grant for a process/reset instance.
// Retrying Acquire with the same instance ID must return exactly the same epoch.
// Renew may refresh authorization but must preserve every producer field and
// absolute deadline, including when renewing an old queued epoch during grace.
type MetricGrantProvider interface {
	Acquire(context.Context, string) (MetricGrant, error)
	Renew(context.Context, string) (MetricGrant, error)
}

func (g MetricGrant) validate(now time.Time) error {
	for _, v := range []string{g.ResourceID, g.ProducerID, g.ProducerEpoch} {
		if v == "" || strings.TrimSpace(v) != v || len(v) > 256 || strings.ContainsRune(v, 0) {
			return errors.New("invalid metric grant identity")
		}
	}
	if g.EpochStartedAt <= 0 || g.EpochStartedAt > now.Add(5*time.Minute).Unix() || g.EpochNotAfter <= g.EpochStartedAt || g.EpochNotAfter-g.EpochStartedAt > 86400 || g.RetryUntil < g.EpochNotAfter || g.RetryUntil-g.EpochNotAfter > 86400 || now.Unix() >= g.RetryUntil {
		return errors.New("invalid or expired metric grant deadlines")
	}
	// This is a local consistency check, not signature verification. The host
	// authenticates this opaque token again for every export and grant request.
	parts := strings.Split(g.Token, ".")
	if len(parts) != 2 {
		return errors.New("invalid metric grant token")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("invalid metric grant token body")
	}
	var claims struct {
		Producer *MetricGrant `json:"metric_producer"`
	}
	if err = json.Unmarshal(body, &claims); err != nil || claims.Producer == nil || !g.sameIdentity(*claims.Producer) {
		return errors.New("metric grant response differs from token body")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != 32 {
		return errors.New("invalid metric grant token signature encoding")
	}
	return nil
}
func (g MetricGrant) sameIdentity(other MetricGrant) bool {
	g.Token = ""
	other.Token = ""
	return g == other
}

// GRPCMetricGrantProvider uses the authenticated platform endpoint. Its initial
// token is the existing tenant runtime token. Successful grants refresh that
// authorization for later requests, without changing queued RPC identity.
type GRPCMetricGrantProvider struct {
	client       grpc.ClientConnInterface
	mu           sync.Mutex
	token        string
	grants       map[string]MetricGrant
	instances    map[string]MetricGrant
	activeEpoch  string
	tokenUpdated func(string)
	now          func() time.Time
}

func NewGRPCMetricGrantProvider(client grpc.ClientConnInterface, initialToken string) (*GRPCMetricGrantProvider, error) {
	if !metricInterfacePresent(client) || strings.TrimSpace(initialToken) == "" {
		return nil, errors.New("metric grant endpoint and initial token are required")
	}
	return &GRPCMetricGrantProvider{client: client, token: initialToken, grants: make(map[string]MetricGrant), instances: make(map[string]MetricGrant), now: time.Now}, nil
}
func (p *GRPCMetricGrantProvider) Acquire(ctx context.Context, instanceID string) (MetricGrant, error) {
	if len(instanceID) != 32 {
		return MetricGrant{}, errors.New("metric process instance must be 128-bit hex")
	}
	if _, err := hex.DecodeString(instanceID); err != nil {
		return MetricGrant{}, errors.New("invalid metric process instance")
	}
	return p.call(ctx, "Acquire", "producer_instance_id", instanceID)
}
func (p *GRPCMetricGrantProvider) Renew(ctx context.Context, epoch string) (MetricGrant, error) {
	if epoch == "" {
		return MetricGrant{}, errors.New("metric epoch is required")
	}
	return p.call(ctx, "Renew", "producer_epoch", epoch)
}
func (p *GRPCMetricGrantProvider) call(ctx context.Context, method, key, value string) (MetricGrant, error) {
	p.mu.Lock()
	token := p.token
	expected, hasExpected := p.instances[value]
	if method == "Renew" {
		grant, ok := p.grants[value]
		if !ok {
			p.mu.Unlock()
			return MetricGrant{}, errors.New("unknown metric renewal epoch")
		}
		token = grant.Token
		expected, hasExpected = grant, true
	}
	p.mu.Unlock()
	request, _ := structpb.NewStruct(map[string]any{key: value})
	response := new(structpb.Struct)
	if err := p.client.Invoke(metricAuthContext(ctx, token), "/gobeyond.telemetry.v1.MetricProducerGrants/"+method, request, response); err != nil {
		return MetricGrant{}, err
	}
	fields := response.AsMap()
	text := func(k string) string { v, _ := fields[k].(string); return v }
	seconds := func(k string) (int64, error) {
		v, ok := fields[k].(float64)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 9007199254740991 || math.Trunc(v) != v {
			return 0, fmt.Errorf("invalid grant %s", k)
		}
		return int64(v), nil
	}
	g := MetricGrant{Token: text("token"), ResourceID: text("resource_id"), ProducerID: text("producer_id"), ProducerEpoch: text("producer_epoch")}
	var err error
	if g.EpochStartedAt, err = seconds("epoch_started_at"); err != nil {
		return MetricGrant{}, err
	}
	if g.EpochNotAfter, err = seconds("epoch_not_after"); err != nil {
		return MetricGrant{}, err
	}
	if g.RetryUntil, err = seconds("retry_until"); err != nil {
		return MetricGrant{}, err
	}
	if err = g.validate(p.now()); err != nil {
		return MetricGrant{}, err
	}
	if hasExpected && !g.sameIdentity(expected) {
		return MetricGrant{}, errors.New("metric grant changed fixed epoch identity or deadlines")
	}
	p.mu.Lock()
	active := method == "Acquire" || p.activeEpoch == g.ProducerEpoch
	if active {
		p.token = g.Token
		p.activeEpoch = g.ProducerEpoch
	}
	for epoch, grant := range p.grants {
		if p.now().Unix() >= grant.RetryUntil {
			delete(p.grants, epoch)
			for instance, old := range p.instances {
				if old.ProducerEpoch == epoch {
					delete(p.instances, instance)
				}
			}
		}
	}
	p.grants[g.ProducerEpoch] = g
	if method == "Acquire" {
		p.instances[value] = g
	}
	// Serialize the credential observer with active-epoch selection. An old
	// renewal finishing concurrently may not overwrite a newer Acquire token.
	if active && p.tokenUpdated != nil {
		p.tokenUpdated(g.Token)
	}
	p.mu.Unlock()
	return g, nil
}

func metricAuthContext(ctx context.Context, token string) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Set(tokenHeader, token)
	return metadata.NewOutgoingContext(ctx, md)
}
