-- Track when wishlist notifications were last sent to avoid spam.
ALTER TABLE wishlist ADD COLUMN price_drop_notified_at DATETIME;
ALTER TABLE wishlist ADD COLUMN back_in_stock_notified_at DATETIME;
