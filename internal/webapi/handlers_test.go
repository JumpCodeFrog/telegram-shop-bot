package webapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/payment"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// ---- fakes ----------------------------------------------------------------

type fakeCatalog struct {
	categories []storage.Category
	products   map[int64]storage.Product
}

func (f *fakeCatalog) ListCategories(context.Context) ([]storage.Category, error) {
	return f.categories, nil
}

func (f *fakeCatalog) ListProductsPaged(_ context.Context, categoryID int64, limit, offset int) ([]storage.Product, int, error) {
	var all []storage.Product
	for _, p := range f.products {
		if p.CategoryID == categoryID {
			all = append(all, p)
		}
	}
	total := len(all)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}

func (f *fakeCatalog) GetProduct(_ context.Context, id int64) (*storage.Product, error) {
	p, ok := f.products[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &p, nil
}

// fakeCart is a map-backed CartService with the real ChangeQuantity semantics.
type fakeCart struct {
	products map[int64]storage.Product
	items    map[int64]map[int64]int // userID → productID → qty
}

func (f *fakeCart) userItems(userID int64) map[int64]int {
	if f.items == nil {
		f.items = make(map[int64]map[int64]int)
	}
	if f.items[userID] == nil {
		f.items[userID] = make(map[int64]int)
	}
	return f.items[userID]
}

func (f *fakeCart) Get(_ context.Context, userID int64) (*shop.CartView, error) {
	view := &shop.CartView{Items: []shop.CartItemView{}}
	for pid, qty := range f.userItems(userID) {
		p := f.products[pid]
		view.Items = append(view.Items, shop.CartItemView{Product: p, Quantity: qty})
		view.TotalUSD += p.PriceUSD * float64(qty)
		view.TotalStars += p.PriceStars * qty
	}
	return view, nil
}

func (f *fakeCart) ChangeQuantity(_ context.Context, userID, productID int64, delta int) error {
	items := f.userItems(userID)
	newQty := items[productID] + delta
	if newQty <= 0 {
		delete(items, productID)
		return nil
	}
	p, ok := f.products[productID]
	if !ok {
		return storage.ErrNotFound
	}
	if delta > 0 && (!p.IsActive || p.Stock < newQty) {
		return storage.ErrProductOutOfStock
	}
	items[productID] = newQty
	return nil
}

func (f *fakeCart) Remove(_ context.Context, userID, productID int64) error {
	delete(f.userItems(userID), productID)
	return nil
}

func (f *fakeCart) Clear(_ context.Context, userID int64) error {
	f.items[userID] = make(map[int64]int)
	return nil
}

type fakeOrders struct {
	nextID  int64
	orders  map[int64]*storage.Order
	created []int64
}

func (f *fakeOrders) CreateFromCart(_ context.Context, userID int64, view *shop.CartView, promo *storage.PromoCode) (int64, error) {
	if len(view.Items) == 0 {
		return 0, storage.ErrEmptyCart
	}
	f.nextID++
	totalUSD, totalStars := view.TotalUSD, view.TotalStars
	promoCode := ""
	if promo != nil {
		totalUSD = totalUSD * float64(100-promo.Discount) / 100
		totalStars = totalStars * (100 - promo.Discount) / 100
		promoCode = promo.Code
	}
	var items []storage.OrderItem
	for _, it := range view.Items {
		items = append(items, storage.OrderItem{
			ProductID: it.Product.ID, ProductName: it.Product.Name, Quantity: it.Quantity, PriceUSD: it.Product.PriceUSD,
		})
	}
	if f.orders == nil {
		f.orders = make(map[int64]*storage.Order)
	}
	f.orders[f.nextID] = &storage.Order{
		ID: f.nextID, UserID: userID, Status: storage.OrderStatusPending,
		TotalUSD: totalUSD, TotalStars: totalStars, PromoCode: promoCode, Items: items,
	}
	f.created = append(f.created, f.nextID)
	return f.nextID, nil
}

func (f *fakeOrders) GetOrder(_ context.Context, orderID int64) (*storage.Order, error) {
	o, ok := f.orders[orderID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return o, nil
}

func (f *fakeOrders) GetUserOrders(_ context.Context, userID int64) ([]storage.Order, error) {
	var out []storage.Order
	for _, o := range f.orders {
		if o.UserID == userID {
			out = append(out, *o)
		}
	}
	return out, nil
}

type fakeUsers struct{ upserted *storage.User }

func (f *fakeUsers) Upsert(_ context.Context, u *storage.User) error {
	u.ID = 7
	u.LoyaltyPts = 120
	u.LoyaltyLevel = "silver"
	f.upserted = u
	return nil
}

func (f *fakeUsers) GetByTelegramID(context.Context, int64) (*storage.User, error) { return nil, nil }

type fakePromos struct {
	promos map[string]*storage.PromoCode
	used   map[int64]bool
}

func (f *fakePromos) GetPromoByCode(_ context.Context, code string) (*storage.PromoCode, error) {
	p, ok := f.promos[code]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return p, nil
}

func (f *fakePromos) HasUserUsedPromo(_ context.Context, promoID, _ int64) (bool, error) {
	return f.used[promoID], nil
}

type fakeReviews struct {
	avg   float64
	count int64
}

func (f *fakeReviews) ProductRating(context.Context, int64) (float64, int64, error) {
	return f.avg, f.count, nil
}

type fakePhotos struct{ photos []storage.ProductPhoto }

func (f *fakePhotos) List(context.Context, int64) ([]storage.ProductPhoto, error) {
	return f.photos, nil
}

type fakeI18n struct{}

func (fakeI18n) T(_, key string) string { return key }

func (fakeI18n) Tf(_, key string, args ...any) string {
	return fmt.Sprintf(key, args...)
}

func (fakeI18n) Dict(lang string) map[string]string {
	return map[string]string{"lang": lang, "webapp_title": "Shop"}
}

type fakeTg struct {
	endpoint string
	params   tgbotapi.Params
	link     string
}

func (f *fakeTg) MakeRequest(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
	f.endpoint = endpoint
	f.params = params
	raw, _ := json.Marshal(f.link)
	return &tgbotapi.APIResponse{Ok: true, Result: raw}, nil
}

type fakeCrypto struct {
	configured bool
	payURL     string
}

func (f *fakeCrypto) Configured() bool { return f.configured }

func (f *fakeCrypto) CreateInvoice(_ context.Context, orderID int64, _ float64, _ string) (*payment.Invoice, error) {
	return &payment.Invoice{PayURL: f.payURL, InvoiceID: fmt.Sprintf("inv-%d", orderID)}, nil
}

type fakeFiles struct{}

func (fakeFiles) GetFileDirectURL(fileID string) (string, error) {
	return "https://files.example/" + fileID, nil
}

// ---- harness --------------------------------------------------------------

type fixture struct {
	server *Server
	tg     *fakeTg
	crypto *fakeCrypto
	orders *fakeOrders
	cart   *fakeCart
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	products := map[int64]storage.Product{
		1: {ID: 1, CategoryID: 10, Name: "Mug", PriceUSD: 5, PriceStars: 250, Stock: 3, IsActive: true, PhotoURL: "https://img.example/mug.png"},
		2: {ID: 2, CategoryID: 10, Name: "Tee", PriceUSD: 10, PriceStars: 500, Stock: 1, IsActive: true, PhotoURL: "AgACfileid"},
		3: {ID: 3, CategoryID: 11, Name: "Pro Sub", PriceUSD: 2, PriceStars: 100, Stock: 99, IsActive: true, SubPeriodDays: 30},
	}
	cart := &fakeCart{products: products}
	orders := &fakeOrders{}
	tg := &fakeTg{link: "https://t.me/$invoice_link"}
	crypto := &fakeCrypto{configured: true, payURL: "https://pay.crypt.bot/inv"}
	srv := New(Deps{
		Auth: NewAuthenticator(testBotToken, DefaultAuthTTL),
		Catalog: &fakeCatalog{
			categories: []storage.Category{{ID: 10, Name: "Merch", Emoji: "🎁"}},
			products:   products,
		},
		Cart:    cart,
		Orders:  orders,
		Users:   &fakeUsers{},
		Promos:  &fakePromos{promos: map[string]*storage.PromoCode{"SALE10": {ID: 1, Code: "SALE10", Discount: 10}}},
		Reviews: &fakeReviews{avg: 4.5, count: 12},
		Photos:  &fakePhotos{photos: []storage.ProductPhoto{{ID: 1, ProductID: 2, FileID: "extra-photo"}}},
		I18n:    fakeI18n{},
		Tg:      tg,
		Crypto:  crypto,
		Files:   fakeFiles{},
	}, nil)
	return &fixture{server: srv, tg: tg, crypto: crypto, orders: orders, cart: cart}
}

func (f *fixture) request(t *testing.T, method, target, body string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if authed {
		req.Header.Set("Authorization", "tma "+testInitData(t, time.Now()))
	}
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, rec.Body.String())
	}
	return out
}

// ---- tests ----------------------------------------------------------------

func TestAPIRejectsUnauthenticatedRequests(t *testing.T) {
	f := newFixture(t)
	for _, target := range []string{"/api/me", "/api/catalog", "/api/cart", "/api/products?category=10"} {
		rec := f.request(t, http.MethodGet, target, "", false)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want 401", target, rec.Code)
		}
		if got := decodeJSON(t, rec)["error"]; got != "webapp_err_unauthorized" {
			t.Errorf("GET %s error = %v, want webapp_err_unauthorized", target, got)
		}
	}
}

func TestAPIRejectsTamperedAuth(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog", nil)
	initData := testInitData(t, time.Now())
	req.Header.Set("Authorization", "tma "+strings.Replace(initData, "auth_date=", "auth_date=9", 1))
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestI18nEndpointIsPublic(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodGet, "/api/i18n?lang=de", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := decodeJSON(t, rec)["lang"]; got != "de" {
		t.Errorf("dict lang = %v, want de", got)
	}
}

func TestMeReturnsProfile(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodGet, "/api/me", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeJSON(t, rec)
	if got["telegram_id"] != float64(42) {
		t.Errorf("telegram_id = %v, want 42", got["telegram_id"])
	}
	if got["loyalty_points"] != float64(120) || got["loyalty_level"] != "silver" {
		t.Errorf("loyalty = %v/%v, want 120/silver", got["loyalty_points"], got["loyalty_level"])
	}
	if got["language"] != "ru" {
		t.Errorf("language = %v, want ru", got["language"])
	}
}

func TestCatalogListsCategories(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodGet, "/api/catalog", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cats := decodeJSON(t, rec)["categories"].([]any)
	if len(cats) != 1 {
		t.Fatalf("len(categories) = %d, want 1", len(cats))
	}
	cat := cats[0].(map[string]any)
	if cat["id"] != float64(10) || cat["name"] != "Merch" {
		t.Errorf("category = %v, want id=10 name=Merch", cat)
	}
}

func TestProductsPaged(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodGet, "/api/products?category=10", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON(t, rec)
	if got["total"] != float64(2) {
		t.Errorf("total = %v, want 2", got["total"])
	}
	if len(got["products"].([]any)) != 2 {
		t.Errorf("len(products) = %d, want 2", len(got["products"].([]any)))
	}

	// Missing/invalid category → 400 with an i18n key.
	rec = f.request(t, http.MethodGet, "/api/products", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no category: status = %d, want 400", rec.Code)
	}
	if decodeJSON(t, rec)["error"] != "webapp_err_bad_request" {
		t.Errorf("no category: error = %v, want webapp_err_bad_request", decodeJSON(t, rec)["error"])
	}
}

func TestProductCardHasRatingAndPhotos(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodGet, "/api/products/2", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON(t, rec)
	if got["rating_avg"] != 4.5 || got["rating_count"] != float64(12) {
		t.Errorf("rating = %v/%v, want 4.5/12", got["rating_avg"], got["rating_count"])
	}
	photos := got["photos"].([]any)
	// Cover (file_id → proxied) + one extra gallery photo.
	if len(photos) != 2 {
		t.Fatalf("len(photos) = %d, want 2", len(photos))
	}
	if photos[0] != "/api/photo/AgACfileid" || photos[1] != "/api/photo/extra-photo" {
		t.Errorf("photos = %v, want proxied /api/photo/ refs", photos)
	}

	rec = f.request(t, http.MethodGet, "/api/products/999", "", true)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing product: status = %d, want 404", rec.Code)
	}
	if decodeJSON(t, rec)["error"] != "webapp_err_not_found" {
		t.Errorf("missing product: error = %v, want webapp_err_not_found", decodeJSON(t, rec)["error"])
	}
}

func TestCartAddUpdateRemoveFlow(t *testing.T) {
	f := newFixture(t)

	// Add one Mug (delta defaults to 1).
	rec := f.request(t, http.MethodPost, "/api/cart", `{"product_id":1}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("add: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Increment by 2 → qty 3, totals recomputed.
	rec = f.request(t, http.MethodPost, "/api/cart", `{"product_id":1,"delta":2}`, true)
	got := decodeJSON(t, rec)
	if got["total_stars"] != float64(750) {
		t.Errorf("total_stars = %v, want 750", got["total_stars"])
	}

	// Over stock → 409 out of stock key.
	rec = f.request(t, http.MethodPost, "/api/cart", `{"product_id":1,"delta":5}`, true)
	if rec.Code != http.StatusConflict {
		t.Errorf("over stock: status = %d, want 409", rec.Code)
	}
	if decodeJSON(t, rec)["error"] != "webapp_err_out_of_stock" {
		t.Errorf("over stock: error = %v, want webapp_err_out_of_stock", decodeJSON(t, rec)["error"])
	}

	// GET reflects state.
	rec = f.request(t, http.MethodGet, "/api/cart", "", true)
	items := decodeJSON(t, rec)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].(map[string]any)["quantity"] != float64(3) {
		t.Errorf("quantity = %v, want 3", items[0].(map[string]any)["quantity"])
	}

	// DELETE one position.
	rec = f.request(t, http.MethodDelete, "/api/cart?product_id=1", "", true)
	if got := decodeJSON(t, rec); len(got["items"].([]any)) != 0 {
		t.Errorf("after delete: items = %v, want empty", got["items"])
	}
}

func TestCartRejectsBadBody(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodPost, "/api/cart", `{"product_id":`, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	rec = f.request(t, http.MethodPost, "/api/cart", `{"product_id":1,"delta":0}`, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("delta=0: status = %d, want 400", rec.Code)
	}
}

func TestCheckoutStarsCreatesInvoiceLink(t *testing.T) {
	f := newFixture(t)
	f.request(t, http.MethodPost, "/api/cart", `{"product_id":1,"delta":2}`, true)

	rec := f.request(t, http.MethodPost, "/api/checkout", `{"method":"stars","promo":"sale10"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeJSON(t, rec)
	if got["invoice_link"] != "https://t.me/$invoice_link" {
		t.Errorf("invoice_link = %v, want stub link", got["invoice_link"])
	}
	if got["order_id"] != float64(1) {
		t.Errorf("order_id = %v, want 1", got["order_id"])
	}

	if f.tg.endpoint != "createInvoiceLink" {
		t.Errorf("endpoint = %q, want createInvoiceLink", f.tg.endpoint)
	}
	if f.tg.params["currency"] != "XTR" {
		t.Errorf("currency = %q, want XTR", f.tg.params["currency"])
	}
	if f.tg.params["payload"] != "1" {
		t.Errorf("payload = %q, want order id 1", f.tg.params["payload"])
	}
	if _, ok := f.tg.params["subscription_period"]; ok {
		t.Error("subscription_period set for a regular order")
	}
	// 10% promo applied: 2×250 stars → 450.
	if !strings.Contains(f.tg.params["prices"], "450") {
		t.Errorf("prices = %q, want discounted 450 stars", f.tg.params["prices"])
	}
}

func TestCheckoutSubscription(t *testing.T) {
	f := newFixture(t)
	f.request(t, http.MethodPost, "/api/cart", `{"product_id":3}`, true)

	// Crypto for a subscription is rejected.
	rec := f.request(t, http.MethodPost, "/api/checkout", `{"method":"crypto"}`, true)
	if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "webapp_err_sub_stars_only" {
		t.Errorf("crypto sub: status/error = %d/%v, want 400/webapp_err_sub_stars_only", rec.Code, decodeJSON(t, rec)["error"])
	}

	// Stars gets subscription_period.
	rec = f.request(t, http.MethodPost, "/api/checkout", `{"method":"stars"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("stars sub: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if f.tg.params["subscription_period"] != "2592000" {
		t.Errorf("subscription_period = %q, want 2592000", f.tg.params["subscription_period"])
	}
}

func TestCheckoutCrypto(t *testing.T) {
	f := newFixture(t)
	f.request(t, http.MethodPost, "/api/cart", `{"product_id":1}`, true)

	rec := f.request(t, http.MethodPost, "/api/checkout", `{"method":"crypto"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := decodeJSON(t, rec)["invoice_link"]; got != "https://pay.crypt.bot/inv" {
		t.Errorf("invoice_link = %v, want CryptoBot pay URL", got)
	}
}

func TestCheckoutValidation(t *testing.T) {
	f := newFixture(t)

	// Empty cart.
	rec := f.request(t, http.MethodPost, "/api/checkout", `{"method":"stars"}`, true)
	if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "webapp_err_empty_cart" {
		t.Errorf("empty cart: status/error = %d/%v, want 400/webapp_err_empty_cart", rec.Code, decodeJSON(t, rec)["error"])
	}

	// Unknown method.
	rec = f.request(t, http.MethodPost, "/api/checkout", `{"method":"paypal"}`, true)
	if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "webapp_err_method" {
		t.Errorf("bad method: status/error = %d/%v, want 400/webapp_err_method", rec.Code, decodeJSON(t, rec)["error"])
	}

	// Crypto disabled.
	f.crypto.configured = false
	f.request(t, http.MethodPost, "/api/cart", `{"product_id":1}`, true)
	rec = f.request(t, http.MethodPost, "/api/checkout", `{"method":"crypto"}`, true)
	if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "webapp_err_crypto_disabled" {
		t.Errorf("crypto off: status/error = %d/%v, want 400/webapp_err_crypto_disabled", rec.Code, decodeJSON(t, rec)["error"])
	}
	f.crypto.configured = true

	// Unknown promo.
	rec = f.request(t, http.MethodPost, "/api/checkout", `{"method":"stars","promo":"NOPE"}`, true)
	if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "promo_not_found" {
		t.Errorf("bad promo: status/error = %d/%v, want 400/promo_not_found", rec.Code, decodeJSON(t, rec)["error"])
	}
}

func TestCheckoutRejectsOversizedBody(t *testing.T) {
	f := newFixture(t)
	f.request(t, http.MethodPost, "/api/cart", `{"product_id":1}`, true)

	body := `{"method":"stars","promo":"` + strings.Repeat("A", maxBodyBytes) + `"}`
	rec := f.request(t, http.MethodPost, "/api/checkout", body, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPhotoProxyStreamsFile(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNGDATA"))
	}))
	defer upstream.Close()

	f := newFixture(t)
	f.server.deps.Files = staticFileResolver(upstream.URL + "/file")

	rec := f.request(t, http.MethodGet, "/api/photo/AgACfileid", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "PNGDATA" {
		t.Errorf("body = %q, want proxied bytes", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
}

type staticFileResolver string

func (s staticFileResolver) GetFileDirectURL(string) (string, error) { return string(s), nil }
