-- Add expiry support for referral codes.
-- NULL means no expiry (legacy codes remain valid indefinitely).
ALTER TABLE users ADD COLUMN referral_code_expires_at DATETIME;
