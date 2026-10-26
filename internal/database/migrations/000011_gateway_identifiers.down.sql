-- Drop the separate order id column and its indexes.
DROP INDEX IF EXISTS uq_payments_gateway_order_id;
DROP INDEX IF EXISTS idx_payments_gateway_reference;

ALTER TABLE payments DROP COLUMN IF EXISTS gateway_order_id;
