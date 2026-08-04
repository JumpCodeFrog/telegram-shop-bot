-- Personal promo codes: bound_user_id is the Telegram user ID of the only
-- user allowed to see and apply the code. NULL = public code (behaviour unchanged).
ALTER TABLE promo_codes ADD COLUMN bound_user_id INTEGER;

-- One referral award per referred user, ever: the PRIMARY KEY on
-- referred_user_id makes INSERT OR IGNORE an idempotent "first paid order"
-- gate even under concurrent payment confirmations.
CREATE TABLE IF NOT EXISTS referral_awards (
    referred_user_id INTEGER PRIMARY KEY REFERENCES users(id),
    referrer_id      INTEGER NOT NULL REFERENCES users(id),
    points           INTEGER NOT NULL,
    promo_code       TEXT NOT NULL,
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
