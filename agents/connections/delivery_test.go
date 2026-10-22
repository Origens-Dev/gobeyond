package connections

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

func TestDeliveryAuthenticationAndReplayBounds(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	body := []byte(`{"event_id":"event-1"}`)
	now := time.Unix(1800000000, 0)
	h := http.Header{}
	if err := SignDelivery(h, key, "event-1", body, now); err != nil {
		t.Fatal(err)
	}
	if id, err := VerifyDelivery(h, key, body, now.Add(4*time.Minute)); err != nil || id != "event-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	for name, check := range map[string]func(http.Header) error{
		"body substitution": func(h http.Header) error { _, err := VerifyDelivery(h, key, []byte("changed"), now); return err },
		"wrong environment key": func(h http.Header) error {
			_, err := VerifyDelivery(h, bytes.Repeat([]byte{8}, 32), body, now)
			return err
		},
		"expired": func(h http.Header) error {
			_, err := VerifyDelivery(h, key, body, now.Add(5*time.Minute+time.Second))
			return err
		},
		"future": func(h http.Header) error {
			_, err := VerifyDelivery(h, key, body, now.Add(-31*time.Second))
			return err
		},
		"duplicate header": func(h http.Header) error {
			h.Add(DeliveryIDHeader, "event-2")
			_, err := VerifyDelivery(h, key, body, now)
			return err
		},
		"id substitution": func(h http.Header) error {
			h.Set(DeliveryIDHeader, "event-2")
			_, err := VerifyDelivery(h, key, body, now)
			return err
		},
		"timestamp normalization": func(h http.Header) error {
			h.Set(DeliveryTimestampHeader, "+1800000000")
			_, err := VerifyDelivery(h, key, body, now)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(h.Clone()); err == nil {
				t.Fatal("forged delivery accepted")
			}
		})
	}
}

func TestDeliveryRequiresEnvironmentKeyAndValidIdentity(t *testing.T) {
	if err := SignDelivery(http.Header{}, []byte("weak"), "event-1", nil, time.Now()); err == nil {
		t.Fatal("weak key accepted")
	}
	if err := SignDelivery(http.Header{}, bytes.Repeat([]byte{1}, 32), "bad\nidentity", nil, time.Now()); err == nil {
		t.Fatal("invalid event id accepted")
	}
}
