package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/bot"
	"shop_bot/internal/config"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

type telegramCall struct {
	Method    string
	Params    url.Values
	MessageID int
}

type fakeTelegramAPI struct {
	mu            sync.Mutex
	calls         []telegramCall
	nextMessageID int
}

func newFakeTelegramAPI() *fakeTelegramAPI {
	return &fakeTelegramAPI{nextMessageID: 100}
}

func (f *fakeTelegramAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		_ = r.ParseForm()
	}

	method := pathMethod(r.URL.Path)
	params := cloneValues(r.Form)

	call := telegramCall{
		Method: method,
		Params: params,
	}

	switch method {
	case "getMe":
		f.writeJSON(w, map[string]any{
			"ok": true,
			"result": map[string]any{
				"id":         999001,
				"is_bot":     true,
				"first_name": "UX Smoke Bot",
				"username":   "ux_smoke_bot",
			},
		})
		return
	case "answerCallbackQuery", "deleteMessage":
		f.record(call)
		f.writeJSON(w, map[string]any{"ok": true, "result": true})
		return
	default:
		call.MessageID = f.nextID(params.Get("message_id"))
		f.record(call)
		f.writeJSON(w, map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": call.MessageID,
				"date":       time.Now().Unix(),
				"chat": map[string]any{
					"id":   mustParseInt64(params.Get("chat_id")),
					"type": "private",
				},
				"text":    params.Get("text"),
				"caption": params.Get("caption"),
			},
		})
	}
}

func (f *fakeTelegramAPI) record(call telegramCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeTelegramAPI) nextID(existing string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	if existing != "" {
		if id, err := strconv.Atoi(existing); err == nil {
			return id
		}
	}

	f.nextMessageID++
	return f.nextMessageID
}

func (f *fakeTelegramAPI) snapshot(from int) []telegramCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	if from >= len(f.calls) {
		return nil
	}

	out := make([]telegramCall, len(f.calls[from:]))
	copy(out, f.calls[from:])
	return out
}

func (f *fakeTelegramAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTelegramAPI) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func pathMethod(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func cloneValues(src url.Values) url.Values {
	dst := make(url.Values, len(src))
	for key, values := range src {
		cp := make([]string, len(values))
		copy(cp, values)
		dst[key] = cp
	}
	return dst
}

func mustParseInt64(raw string) int64 {
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

func seedUsabilityData(db *storage.DB) error {
	conn := db.Conn()

	type categorySeed struct {
		name  string
		emoji string
		sort  int
	}

	categories := []categorySeed{
		{name: "Одежда", emoji: "👕", sort: 1},
		{name: "Аксессуары", emoji: "🎒", sort: 2},
	}

	for _, c := range categories {
		if _, err := conn.Exec(
			`INSERT INTO categories (name, emoji, sort_order, is_active) VALUES (?, ?, ?, 1)`,
			c.name, c.emoji, c.sort,
		); err != nil {
			return err
		}
	}

	rows, err := conn.Query(`SELECT id, name FROM categories ORDER BY sort_order ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	catIDs := map[string]int64{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		catIDs[name] = id
	}

	products := []struct {
		category string
		name     string
		desc     string
		usd      float64
		stars    int
		stock    int
	}{
		{
			category: "Одежда",
			name:     "Футболка базовая",
			desc:     "Хлопок 100%, размеры S-XXL",
			usd:      12.99,
			stars:    649,
			stock:    50,
		},
		{
			category: "Аксессуары",
			name:     "Кепка с вышивкой",
			desc:     "One size, регулируемый ремень",
			usd:      9.99,
			stars:    499,
			stock:    25,
		},
	}

	for _, p := range products {
		if _, err := conn.Exec(
			`INSERT INTO products (category_id, name, description, price_usd, price_stars, stock, is_active)
			 VALUES (?, ?, ?, ?, ?, ?, 1)`,
			catIDs[p.category], p.name, p.desc, p.usd, p.stars, p.stock,
		); err != nil {
			return err
		}
	}

	return nil
}

func commandUpdate(updateID int, chatID, userID int64, text, lang string) tgbotapi.Update {
	entityLength := len(strings.SplitN(text, " ", 2)[0])
	return tgbotapi.Update{
		UpdateID: updateID,
		Message: &tgbotapi.Message{
			MessageID: updateID,
			Chat:      &tgbotapi.Chat{ID: chatID, Type: "private"},
			From: &tgbotapi.User{
				ID:           userID,
				FirstName:    "Thomas",
				UserName:     "thom",
				LanguageCode: lang,
			},
			Text: text,
			Entities: []tgbotapi.MessageEntity{
				{Offset: 0, Length: entityLength, Type: "bot_command"},
			},
		},
	}
}

func callbackUpdate(updateID int, chatID, userID int64, msgID int, data, lang string) tgbotapi.Update {
	return tgbotapi.Update{
		UpdateID: updateID,
		CallbackQuery: &tgbotapi.CallbackQuery{
			ID:   fmt.Sprintf("cb-%d", updateID),
			Data: data,
			From: &tgbotapi.User{
				ID:           userID,
				FirstName:    "Thomas",
				UserName:     "thom",
				LanguageCode: lang,
			},
			Message: &tgbotapi.Message{
				MessageID: msgID,
				Chat:      &tgbotapi.Chat{ID: chatID, Type: "private"},
			},
		},
	}
}

func printCalls(title string, calls []telegramCall) {
	fmt.Printf("\n=== %s ===\n", title)
	if len(calls) == 0 {
		fmt.Println("Нет исходящих действий.")
		return
	}

	for _, call := range calls {
		fmt.Printf("method: %s\n", call.Method)
		switch call.Method {
		case "sendMessage", "editMessageText":
			text := firstNonEmpty(call.Params.Get("text"), call.Params.Get("caption"))
			fmt.Println("text:")
			fmt.Println(text)
			printKeyboard(call.Params.Get("reply_markup"))
		case "sendPhoto":
			fmt.Println("caption:")
			fmt.Println(call.Params.Get("caption"))
			printKeyboard(call.Params.Get("reply_markup"))
		case "editMessageReplyMarkup":
			printKeyboard(call.Params.Get("reply_markup"))
		case "answerCallbackQuery":
			fmt.Printf("callback feedback: %q\n", call.Params.Get("text"))
			if call.Params.Get("show_alert") != "" {
				fmt.Printf("show_alert: %s\n", call.Params.Get("show_alert"))
			}
		case "sendInvoice":
			fmt.Printf("invoice title: %s\n", call.Params.Get("title"))
			fmt.Printf("invoice description: %s\n", call.Params.Get("description"))
			fmt.Printf("invoice payload: %s\n", call.Params.Get("payload"))
			fmt.Printf("invoice prices: %s\n", call.Params.Get("prices"))
		default:
			fmt.Printf("params: %v\n", call.Params)
		}
		fmt.Println()
	}
}

func printKeyboard(raw string) {
	if raw == "" {
		return
	}

	var markup tgbotapi.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(raw), &markup); err != nil {
		fmt.Printf("reply_markup(raw): %s\n", raw)
		return
	}

	fmt.Println("buttons:")
	for _, row := range markup.InlineKeyboard {
		labels := make([]string, 0, len(row))
		for _, btn := range row {
			labels = append(labels, btn.Text)
		}
		fmt.Printf("  [%s]\n", strings.Join(labels, "] ["))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func step(title string, recorder *fakeTelegramAPI, fn func()) []telegramCall {
	before := recorder.count()
	fn()
	time.Sleep(600 * time.Millisecond)
	calls := recorder.snapshot(before)
	printCalls(title, calls)
	return calls
}

func lastMessageID(calls []telegramCall) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].MessageID > 0 {
			return calls[i].MessageID
		}
	}
	return 0
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	tempDir, err := os.MkdirTemp("", "go_tg_shop_usability_*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "shop.db")
	db, err := storage.New(dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	if err := seedUsabilityData(db); err != nil {
		panic(err)
	}

	recorder := newFakeTelegramAPI()
	server := httptest.NewServer(http.HandlerFunc(recorder.serveHTTP))
	defer server.Close()

	api, err := tgbotapi.NewBotAPIWithAPIEndpoint("usability-smoke-token", server.URL+"/bot%s/%s")
	if err != nil {
		panic(err)
	}

	workdir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	cfg := &config.Config{
		BotToken:       "usability-smoke-token",
		DBPath:         dbPath,
		USDToStarsRate: 50,
		LocalesDir:     filepath.Join(workdir, "locales"),
	}

	b, err := bot.NewWithAPI(cfg, api, db, service.NewMetricsService(), storage.NewMemoryFSMStore(), nil)
	if err != nil {
		panic(err)
	}

	const (
		chatID = int64(20001)
		userID = int64(30001)
		lang   = "ru"
	)

	fmt.Println("Telegram usability smoke")
	fmt.Println("Сценарий: /start -> каталог -> товар -> + на карточке -> /cart -> +1 -> checkout -> confirm -> terms/support -> Stars invoice")

	startCalls := step("/start", recorder, func() {
		b.HandleUpdate(commandUpdate(1, chatID, userID, "/start", lang))
	})
	welcomeMsgID := lastMessageID(startCalls)

	step("Каталог из главного меню", recorder, func() {
		b.HandleUpdate(callbackUpdate(2, chatID, userID, welcomeMsgID, "back:catalog", lang))
	})

	step("Список товаров категории", recorder, func() {
		b.HandleUpdate(callbackUpdate(3, chatID, userID, welcomeMsgID, "category:1", lang))
	})

	step("Карточка товара", recorder, func() {
		b.HandleUpdate(callbackUpdate(4, chatID, userID, welcomeMsgID, "product:1", lang))
	})

	step("Плюс на карточке товара", recorder, func() {
		b.HandleUpdate(callbackUpdate(5, chatID, userID, welcomeMsgID, "productqty:plus:1", lang))
	})

	cartCalls := step("/cart", recorder, func() {
		b.HandleUpdate(commandUpdate(6, chatID, userID, "/cart", lang))
	})
	cartMsgID := lastMessageID(cartCalls)

	step("Увеличение количества в корзине", recorder, func() {
		b.HandleUpdate(callbackUpdate(7, chatID, userID, cartMsgID, "cart:plus:1", lang))
	})

	step("Checkout", recorder, func() {
		b.HandleUpdate(callbackUpdate(8, chatID, userID, cartMsgID, "cart:checkout", lang))
	})

	paymentCalls := step("Подтверждение заказа", recorder, func() {
		b.HandleUpdate(callbackUpdate(9, chatID, userID, cartMsgID, "order:confirm", lang))
	})
	paymentMsgID := lastMessageID(paymentCalls)

	step("Условия покупки", recorder, func() {
		b.HandleUpdate(callbackUpdate(10, chatID, userID, paymentMsgID, "terms", lang))
	})

	step("Поддержка по оплате", recorder, func() {
		b.HandleUpdate(commandUpdate(11, chatID, userID, "/paysupport", lang))
	})

	step("Запрос Stars invoice", recorder, func() {
		b.HandleUpdate(callbackUpdate(12, chatID, userID, paymentMsgID, "pay:stars:1", lang))
	})

	fmt.Println("Итог: сценарий пройден локально через фейковый Telegram API без реального клиента.")
}
