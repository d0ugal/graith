package cigate

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestValidateWebhookChecksSignatureAndDeliveryReplay(t *testing.T) {
	secret := []byte("braw secret")
	body := []byte(`{"zen":"canny"}`)
	request := WebhookRequest{
		Event:        "pull_request",
		DeliveryID:   "delivery-braw",
		Signature256: webhookSignature(secret, body),
		Body:         body,
	}
	store := NewMemoryReplayStore()

	delivery, err := ValidateWebhook(secret, request, store)
	if err != nil {
		t.Fatal(err)
	}

	if !delivery.SignatureValidated {
		t.Fatal("SignatureValidated = false, want true")
	}

	if delivery.BodyDigest == "" {
		t.Fatal("BodyDigest is empty")
	}

	_, err = ValidateWebhook(secret, request, store)
	if err == nil || !strings.Contains(err.Error(), "replayed webhook-delivery") {
		t.Fatalf("second ValidateWebhook() error = %v, want delivery replay", err)
	}
}

func TestValidateWebhookRejectsTypedNilReplayStore(t *testing.T) {
	secret := []byte("braw secret")
	body := []byte(`{"zen":"canny"}`)

	var store *MemoryReplayStore

	_, err := ValidateWebhook(secret, WebhookRequest{
		Event:        "pull_request",
		DeliveryID:   "delivery-braw",
		Signature256: webhookSignature(secret, body),
		Body:         body,
	}, store)
	if err == nil || !strings.Contains(err.Error(), "not initialised") {
		t.Fatalf("ValidateWebhook() error = %v, want typed nil replay rejection", err)
	}
}

func TestValidateWebhookRejectsBadInputs(t *testing.T) {
	secret := []byte("thrawn secret")
	body := []byte(`{"zen":"dreich"}`)

	tests := map[string]struct {
		secret []byte
		req    WebhookRequest
		want   string
	}{
		"missing secret": {
			secret: nil,
			req: WebhookRequest{
				Event:        "pull_request",
				DeliveryID:   "delivery",
				Signature256: webhookSignature(secret, body),
				Body:         body,
			},
			want: "webhook secret",
		},
		"missing replay store": {
			secret: secret,
			req: WebhookRequest{
				Event:        "pull_request",
				DeliveryID:   "delivery",
				Signature256: webhookSignature(secret, body),
				Body:         body,
			},
			want: "replay store",
		},
		"bad signature": {
			secret: secret,
			req: WebhookRequest{
				Event:        "pull_request",
				DeliveryID:   "delivery",
				Signature256: webhookSignature([]byte("other secret"), body),
				Body:         body,
			},
			want: "signature mismatch",
		},
		"missing delivery": {
			secret: secret,
			req: WebhookRequest{
				Event:        "pull_request",
				Signature256: webhookSignature(secret, body),
				Body:         body,
			},
			want: "delivery id",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var store ReplayStore = NewMemoryReplayStore()
			if name == "missing replay store" {
				store = nil
			}

			_, err := ValidateWebhook(test.secret, test.req, store)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateWebhook() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func webhookSignature(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
