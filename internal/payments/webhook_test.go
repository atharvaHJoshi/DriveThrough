package payments

import (
	"encoding/json"
	"testing"
)

// TestWebhookIdentifiers is the regression test for the payload-shape bug.
//
// Razorpay nests every entity under payload.<kind>.entity. A flat read of
// payload.payment.id yields "", so the handler looked the payment up by an empty
// gateway ref, missed, and returned an error — which made Razorpay redeliver
// the event forever while the payment was never captured.
func TestWebhookIdentifiers(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantOrder string
		wantPay   string
	}{
		{
			name: "payment.captured carries both order and payment ids",
			body: `{
				"entity": "event",
				"event": "payment.captured",
				"contains": ["payment"],
				"payload": {
					"payment": {
						"entity": {
							"id": "pay_ABC123",
							"order_id": "order_XYZ789",
							"method": "upi",
							"amount": 50000
						}
					}
				}
			}`,
			wantOrder: "order_XYZ789",
			wantPay:   "pay_ABC123",
		},
		{
			name: "payment.failed has the same shape",
			body: `{
				"event": "payment.failed",
				"payload": {
					"payment": {
						"entity": {
							"id": "pay_FAIL01",
							"order_id": "order_FAIL99",
							"method": "card"
						}
					}
				}
			}`,
			wantOrder: "order_FAIL99",
			wantPay:   "pay_FAIL01",
		},
		{
			name: "refund.processed references the payment with no order",
			body: `{
				"event": "refund.processed",
				"payload": {
					"refund": {
						"entity": {
							"id": "rfnd_1",
							"payment_id": "pay_ABC123",
							"amount": 50000,
							"status": "processed"
						}
					}
				}
			}`,
			wantOrder: "",
			wantPay:   "pay_ABC123",
		},
		{
			name: "payment entity wins over a coincident refund reference",
			body: `{
				"event": "refund.processed",
				"payload": {
					"payment": {"entity": {"id": "pay_PRIMARY", "order_id": "order_1"}},
					"refund":  {"entity": {"id": "rfnd_1", "payment_id": "pay_OTHER"}}
				}
			}`,
			wantOrder: "order_1",
			wantPay:   "pay_PRIMARY",
		},
		{
			name: "order entity alone is resolvable",
			body: `{
				"event": "order.paid",
				"payload": {
					"order": {"entity": {"id": "order_ONLY"}}
				}
			}`,
			wantOrder: "order_ONLY",
			wantPay:   "",
		},
		{
			name:      "empty payload yields no identifiers and no panic",
			body:      `{"event": "subscription.activated", "payload": {}}`,
			wantOrder: "",
			wantPay:   "",
		},
		{
			name:      "missing payload key entirely",
			body:      `{"event": "subscription.activated"}`,
			wantOrder: "",
			wantPay:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var e webhookEvent
			if err := json.Unmarshal([]byte(tc.body), &e); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			orderID, paymentID := e.identifiers()
			if orderID != tc.wantOrder {
				t.Errorf("order id = %q, want %q", orderID, tc.wantOrder)
			}
			if paymentID != tc.wantPay {
				t.Errorf("payment id = %q, want %q", paymentID, tc.wantPay)
			}
		})
	}
}

// TestWebhookRejectsMalformedInput makes sure a non-JSON or truncated body is a
// parse error rather than an event with empty identifiers that then fails
// somewhere less obvious.
func TestWebhookRejectsMalformedInput(t *testing.T) {
	for _, body := range []string{"", "{", "not json", `{"payload":`} {
		var e webhookEvent
		if err := json.Unmarshal([]byte(body), &e); err == nil {
			t.Errorf("expected a parse error for %q", body)
		}
	}
}

// TestWebhookToleratesUnknownFields guards forward compatibility: Razorpay adds
// fields to the envelope regularly, and an unknown key must not fail the
// delivery.
func TestWebhookToleratesUnknownFields(t *testing.T) {
	body := `{
		"entity": "event",
		"event": "payment.captured",
		"future_field": {"nested": [1, 2, 3]},
		"payload": {
			"payment": {
				"entity": {
					"id": "pay_NEW",
					"order_id": "order_NEW",
					"brand_new_attribute": "value"
				}
			},
			"unknown_payload_kind": {"x": 1}
		}
	}`

	var e webhookEvent
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("unknown fields must not break parsing: %v", err)
	}
	orderID, paymentID := e.identifiers()
	if orderID != "order_NEW" || paymentID != "pay_NEW" {
		t.Errorf("identifiers = (%q, %q), want (order_NEW, pay_NEW)", orderID, paymentID)
	}
}
