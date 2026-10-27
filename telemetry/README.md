# Authenticated metric export

Metrics are opt-in alongside the existing trace exporter. The default installer
uses the same platform OTLP/gRPC endpoint and runtime token. It obtains producer
capabilities from `/gobeyond.telemetry.v1.MetricProducerGrants/Acquire` and renews
through `/gobeyond.telemetry.v1.MetricProducerGrants/Renew`. Requests/responses are
protobuf `Struct`. Acquire sends `producer_instance_id` (random 128-bit hex);
Renew sends `producer_epoch`. Both authenticate with
`x-gobeyond-telemetry-token`. The flat response contains `token`, `resource_id`,
`producer_id`, `producer_epoch`, `epoch_started_at`, `epoch_not_after`, and
`retry_until`; timestamps are integer Unix seconds. No SDK key signs identity.

Trace RPC credentials follow the latest active metric capability. Renewing an
old queued epoch updates only that queue's authorization and cannot replace the
active trace credential. Metrics-disabled runtimes retain their existing initial
token behavior. Authentication is supplied once per trace RPC; no static auth
header competes with the refreshed credential.

The platform must persist Acquire's process-instance binding across proxy
restart. A new producer process/reset gets a new instance. Renew keeps all six
producer identity/deadline fields fixed. The SDK checks response/token-body
consistency; the host remains the signature authority. Old queued epochs renew
with their own token during their original retry grace. A reset never assigns
new identity to an existing queued body. A new point's end may not exceed its
selected signed epoch's `epoch_not_after`. At expiry, a fresh capability is
acquired before the next whole collected delta is bound to an epoch. The first
new delta can start at the preceding SDK collection boundary, before capability
mint: `epoch_started_at` is control metadata, not an event-time lower bound.
The SDK already resets delta aggregation at every collection. Consecutive
intervals are neither split nor clamped.

Enablement requires explicit settings:

| Variable | Meaning |
| --- | --- |
| `GOBEYOND_OTEL_METRICS_ENABLED` | `1` enables the metrics provider |
| `GOBEYOND_OTEL_METRICS_INTERVAL_MS` | Positive collection interval, at most 60000 ms |
| `GOBEYOND_OTEL_METRICS_RPC_TIMEOUT_MS` | Positive export/flush deadline, at most 5000 ms |
| `GOBEYOND_OTEL_METRICS_MAX_PENDING_RPCS` | Positive retained RPC ceiling, at most 1024 |
| `GOBEYOND_OTEL_METRICS_MAX_RETAINED_BYTES` | Retained serialized retry-body budget |
| `GOBEYOND_OTEL_METRICS_MAX_RPC_BYTES` | Positive wire-body ceiling, at most 262144 and no greater than the retained budget |
| `GOBEYOND_OTEL_METRICS_HISTOGRAM_BOUNDS` | Comma-separated finite increasing bounds, at most 64; explicitly empty means only unbounded tails |

These settings have no implicit production defaults. The queue-byte budget does
not bound the SDK's instrument aggregation or transient serialization memory.
The queue resides in this process; unacknowledged data is not a durable local
outbox. Failed shutdown reports unresolved work. This does not change the host's
AWS-recoverable OTLP acknowledgement contract.

Counters use monotonic delta sums, gauges preserve observed zero, and histograms
use explicit delta populations. Cumulative/nonmonotonic sums, exponential
histograms, nonfinite numbers, non-scalar attributes, nonempty instrumentation
scope attributes (the current metric contract has no scope-attribute field), and user-supplied
`gobeyond.metric.point_id` are rejected explicitly. Every point receives that
reserved identity before batching. Whole serialized requests remain immutable
through retries, including ambiguous failures. Current contract-v2 additionally limits each RPC to63 points: at4KiB canonical
point plus at most64 bytes of normalized envelope,63 points fit256KiB. Each point
must still satisfy its individual payload/attribute limits; raw OTLP size alone
does not establish this. Smaller byte budgets still split earlier.
Splitting occurs only between
identified points and never changes an existing RPC. Exhaustion rejects the new
collection without evicting older work. PartialSuccess never completes a queue
entry. Expired/permanently rejected entries remain unresolved and observable
through `PendingMetrics`; they cannot silently become successful delivery.

The public module requires Go 1.26.0; its qualified compiler is pinned to
Go 1.26.10. The metric SDK is OpenTelemetry 1.47.0, which includes the
[upstream exact-int64 aggregation fix](https://github.com/open-telemetry/opentelemetry-go/pull/8981).
Versions 1.39 through 1.46 round integer counter populations above 2^53 and must
not replace the supported metric SDK. The real SDK acceptance test collects
9007199254740993 exactly and verifies the next collection is a fresh delta.
There is no dependency replacement workaround. Existing trace exporter and gRPC
versions are preserved; the selected module graph and full tests qualify their
compatibility with the updated core SDK.

Point identities use `dense-sequence/v1`: `seq:` plus sixteen lowercase hexadecimal digits, starting at one per signed epoch and shared across every metric kind. The epoch admits at most 16,777,216 assigned points. Exhaustion fails explicitly and requires a fresh producer instance; tokens never wrap or reuse. Frozen queued RPCs retain their original identity. Sequence gaps after rejected local collections are allowed.

`CurrentTelemetryAuthToken(endpoint, fallback)` shares the latest active metric grant with other local telemetry exporters. Renewing an old queued epoch cannot overwrite active credentials.
