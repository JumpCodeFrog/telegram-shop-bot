-- Button style overrides configured via the admin panel.
-- key   — unique button identifier (e.g. "menu_catalog", "cart_checkout")
-- style — Bot API 9.4 style value: "primary" | "success" | "danger" | "" (default)
CREATE TABLE IF NOT EXISTS button_styles (
    key   TEXT NOT NULL PRIMARY KEY,
    style TEXT NOT NULL DEFAULT ''
);
