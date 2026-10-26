-- Separate the gateway order id from the gateway payment id.
--
-- payments.gateway_reference was doing two incompatible jobs. It was written
-- with the order id when the order was created, and then read as though it held
-- the payment id by the three operations that address a payment at the gateway:
--
--   POST /payments/{id}/capture
--   POST /payments/{id}/void
--   POST /payments/{id}/refund
--
-- Those calls were being made with the order id, so capture, void, and refund
-- all addressed the wrong object and would fail against a live gateway. The
-- column then had to be overwritten with the payment id to make the lookups
-- work, which destroyed the order id and made it impossible to correlate the two
-- halves of the transaction.
--
-- gateway_order_id keeps the id we generated the order with, and
-- gateway_reference keeps its meaning as "the gateway's id for this payment",
-- which is what the three operations above and every webhook lookup need.
ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS gateway_order_id TEXT;

-- Backfill from existing rows. Payments that never reached the gateway carry
-- only the order id we handed them, so it is unambiguous for them. For a payment
-- that was captured, gateway_reference may already hold the payment id, so the
-- value is only moved when it looks like an order id.
UPDATE payments
   SET gateway_order_id = gateway_reference
 WHERE gateway_order_id IS NULL
   AND gateway_reference LIKE 'order_%';

-- Payments without a payment id yet are the ones a webhook still has to resolve,
-- so the uniqueness that matters is on the order id we generated.
CREATE UNIQUE INDEX IF NOT EXISTS uq_payments_gateway_order_id
    ON payments (gateway_order_id)
    WHERE gateway_order_id IS NOT NULL;

-- Webhook resolution reads by either identifier, and the payment id is the one
-- the gateway is authoritative about.
CREATE INDEX IF NOT EXISTS idx_payments_gateway_reference
    ON payments (gateway_reference)
    WHERE gateway_reference IS NOT NULL;
