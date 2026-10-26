-- Record that an authorization was deliberately released.
--
-- A booking that ends without a ride — cancelled, or closed as a no-show — is
-- never captured, so the customer is not charged. But the hold itself stays on
-- their card until the issuer expires it, which can be days, and the payment row
-- sits in AUTHORIZED looking like something is still owed.
--
-- VOIDED makes the release explicit and durable, so support can answer "was I
-- charged?" from the record instead of guessing, and a re-run of whatever closed
-- the booking knows the money is already back with the customer.
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_status_check;

ALTER TABLE payments ADD CONSTRAINT payments_status_check
    CHECK (status IN (
        'INITIATED', 'AUTHORIZED', 'CAPTURED', 'VOIDED',
        'FAILED', 'REFUNDED', 'SETTLED'
    ));
