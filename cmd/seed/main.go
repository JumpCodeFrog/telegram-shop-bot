// seed заполняет БД тестовыми данными для локального запуска бота.
// Запуск: go run ./cmd/seed
// Повторный запуск безопасен — INSERT OR IGNORE не создаёт дубликатов.
package main

import (
	"log/slog"
	"os"

	"github.com/joho/godotenv"
	"shop_bot/internal/config"
	"shop_bot/internal/storage"
)

func main() {
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	db, err := storage.New(cfg.DBPath)
	if err != nil {
		slog.Error("db", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	conn := db.Conn()

	// --- Categories ---
	cats := []struct {
		name  string
		emoji string
		sort  int
	}{
		{"Одежда", "👕", 1},
		{"Обувь", "👟", 2},
		{"Аксессуары", "🎒", 3},
	}
	for _, c := range cats {
		if _, err := conn.Exec(
			`INSERT OR IGNORE INTO categories (name, emoji, sort_order, is_active) VALUES (?, ?, ?, 1)`,
			c.name, c.emoji, c.sort,
		); err != nil {
			slog.Error("insert category", "name", c.name, "error", err)
			os.Exit(1)
		}
	}
	slog.Info("categories seeded")

	// --- Products ---
	// Fetch category IDs
	type catRow struct{ id int64; name string }
	var catIDs = map[string]int64{}
	rows, err := conn.Query(`SELECT id, name FROM categories WHERE is_active = 1`)
	if err != nil { slog.Error("query categories", "error", err); os.Exit(1) }
	for rows.Next() {
		var r catRow
		if err := rows.Scan(&r.id, &r.name); err != nil {
			slog.Error("scan category", "error", err)
		}
		catIDs[r.name] = r.id
	}
	rows.Close()

	products := []struct {
		cat   string
		name  string
		desc  string
		usd   float64
		stars int
		stock int
	}{
		{"Одежда", "Футболка базовая", "Хлопок 100%, размеры S-XXL", 12.99, 650, 50},
		{"Одежда", "Худи оверсайз", "Флис, унисекс, чёрный/серый", 29.99, 1500, 30},
		{"Обувь", "Кроссовки беговые", "Лёгкая подошва, дышащий верх", 49.99, 2500, 20},
		{"Обувь", "Слипоны", "На резинке, летние", 19.99, 1000, 40},
		{"Аксессуары", "Рюкзак городской", "Отдел для ноутбука 15.6\"", 34.99, 1750, 25},
		{"Аксессуары", "Кепка с вышивкой", "One size, регулируемый ремень", 9.99, 500, 100},
	}
	for _, p := range products {
		catID, ok := catIDs[p.cat]
		if !ok {
			slog.Warn("category not found", "cat", p.cat)
			continue
		}
		if _, err := conn.Exec(
			`INSERT OR IGNORE INTO products
			 (category_id, name, description, price_usd, price_stars, stock, is_active)
			 VALUES (?, ?, ?, ?, ?, ?, 1)`,
			catID, p.name, p.desc, p.usd, p.stars, p.stock,
		); err != nil {
			slog.Error("insert product", "name", p.name, "error", err)
			os.Exit(1)
		}
	}
	slog.Info("products seeded")

	// --- Promo codes ---
	promos := []struct {
		code     string
		discount int
		maxUses  int
	}{
		{"WELCOME10", 10, 100},
		{"SALE20", 20, 50},
	}
	for _, pr := range promos {
		if _, err := conn.Exec(
			`INSERT OR IGNORE INTO promo_codes
			 (code, discount, max_uses, used_count, is_active)
			 VALUES (?, ?, ?, 0, 1)`,
			pr.code, pr.discount, pr.maxUses,
		); err != nil {
			slog.Error("insert promo", "code", pr.code, "error", err)
			os.Exit(1)
		}
	}
	slog.Info("promo codes seeded")

	slog.Info("seed complete — database ready for testing")
}
