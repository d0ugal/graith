package cigate

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

type WebhookRequest struct {
	Event        string
	DeliveryID   string
	Signature256 string
	Body         []byte
}

func ValidateWebhook(secret []byte, request WebhookRequest, store ReplayStore) (DeliveryContext, error) {
	if len(secret) == 0 {
		return DeliveryContext{}, errors.New("webhook secret is required")
	}

	if store == nil {
		return DeliveryContext{}, errors.New("webhook replay store is required")
	}

	if strings.TrimSpace(request.Event) == "" {
		return DeliveryContext{}, errors.New("webhook event header is required")
	}

	if strings.TrimSpace(request.DeliveryID) == "" {
		return DeliveryContext{}, errors.New("webhook delivery id is required")
	}

	if len(request.Body) == 0 {
		return DeliveryContext{}, errors.New("webhook body is required")
	}

	if err := verifySignature(secret, request.Body, request.Signature256); err != nil {
		return DeliveryContext{}, err
	}

	if err := store.Reserve(ReplayKey{Kind: "webhook-delivery", Value: request.DeliveryID}); err != nil {
		return DeliveryContext{}, err
	}

	bodyDigest := sha256.Sum256(request.Body)

	return DeliveryContext{
		ID:                 request.DeliveryID,
		Event:              request.Event,
		SignatureValidated: true,
		BodyDigest:         hex.EncodeToString(bodyDigest[:]),
	}, nil
}

func verifySignature(secret, body []byte, signature string) error {
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return errors.New("webhook signature must use sha256")
	}

	provided, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return fmt.Errorf("decode webhook signature: %w", err)
	}

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)

	expected := mac.Sum(nil)

	if !hmac.Equal(provided, expected) {
		return errors.New("webhook signature mismatch")
	}

	return nil
}
