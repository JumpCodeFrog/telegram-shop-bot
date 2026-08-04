-- Extra product photos as Telegram file_ids (up to 10 per product).
-- The existing products.photo_url stays as the cover / fallback image.
CREATE TABLE IF NOT EXISTS product_photos (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    product_id  INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    file_id     TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_product_photos_product_id ON product_photos(product_id);
