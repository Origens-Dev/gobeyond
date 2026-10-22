package connections

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	DeliveryIDHeader        = "X-GoBeyond-Event-ID"
	DeliveryTimestampHeader = "X-GoBeyond-Event-Timestamp"
	DeliverySignatureHeader = "X-GoBeyond-Event-Signature"
	MaxDeliveryAge          = 5 * time.Minute
	MaxDeliveryFutureSkew   = 30 * time.Second
)

// SignDelivery authenticates the exact lifecycle/usage envelope. Keys belong to
// one registered provider environment and must never be passed by a browser or
// inferred from the JSON tenant. Receivers still validate event identity, tenant,
// and the durable admission binding after verification, then deduplicate EventID.
func SignDelivery(header http.Header, key []byte, eventID string, body []byte, now time.Time) error {
	if header == nil || len(key) < 32 || !identifier(eventID) || now.IsZero() {
		return errors.New("invalid delivery signing input")
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	header.Set(DeliveryIDHeader, eventID)
	header.Set(DeliveryTimestampHeader, ts)
	header.Set(DeliverySignatureHeader, "v1="+hex.EncodeToString(deliveryMAC(key, ts, eventID, body)))
	return nil
}

// VerifyDelivery verifies a single canonical signature and bounds replay age.
// The request handler must limit the body before reading it. A valid signature
// alone does not authorize a new session; only pre-registered admissions do.
func VerifyDelivery(header http.Header, key []byte, body []byte, now time.Time) (string, error) {
	if len(key) < 32 || now.IsZero() || len(header.Values(DeliveryIDHeader)) != 1 || len(header.Values(DeliveryTimestampHeader)) != 1 || len(header.Values(DeliverySignatureHeader)) != 1 {
		return "", errors.New("invalid delivery authentication")
	}
	id := header.Get(DeliveryIDHeader)
	ts := header.Get(DeliveryTimestampHeader)
	signature := header.Get(DeliverySignatureHeader)
	seconds, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || strconv.FormatInt(seconds, 10) != ts || !identifier(id) || !strings.HasPrefix(signature, "v1=") {
		return "", errors.New("invalid delivery authentication")
	}
	at := time.Unix(seconds, 0)
	if now.Sub(at) > MaxDeliveryAge || at.Sub(now) > MaxDeliveryFutureSkew {
		return "", errors.New("expired delivery authentication")
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "v1="))
	if err != nil || !hmac.Equal(got, deliveryMAC(key, ts, id, body)) {
		return "", errors.New("invalid delivery authentication")
	}
	return id, nil
}

func deliveryMAC(key []byte, timestamp, eventID string, body []byte) []byte {
	digest := sha256.Sum256(body)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(timestamp + "\n" + eventID + "\n" + hex.EncodeToString(digest[:])))
	return mac.Sum(nil)
}
