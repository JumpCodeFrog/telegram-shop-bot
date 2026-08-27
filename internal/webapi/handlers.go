package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/payment"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// maxBodyBytes caps request bodies (spec: 64 KB).
const maxBodyBytes = 64 << 10

// productsPerPage is the page size of GET /api/products.
const productsPerPage = 10

// fileURLTTL is how long a resolved getFile download URL is cached.
// Telegram guarantees at least one hour of validity; stay well under it.
const fileURLTTL = 10 * time.Minute

// subscriptionPeriodSeconds is the only subscription period Telegram accepts (30 days).
const subscriptionPeriodSeconds = 2592000

// CatalogService is the slice of shop.CatalogService the API consumes.
type CatalogService interface {
	ListCategories(ctx context.Context) ([]storage.Category, error)
	ListProductsPaged(ctx context.Context, categoryID int64, limit, offset int) ([]storage.Product, int, error)
	GetProduct(ctx context.Context, id int64) (*storage.Product, error)
}

// CartService is the slice of shop.CartService the API consumes.
type CartService interface {
	Get(ctx context.Context, userID int64) (*shop.CartView, error)
	ChangeQuantity(ctx context.Context, userID, productID int64, delta int) error
	Remove(ctx context.Context, userID, productID int64) error
	Clear(ctx context.Context, userID int64) error
}

// OrderService is the slice of shop.OrderService the API consumes.
type OrderService interface {
	CreateFromCart(ctx context.Context, userID int64, view *shop.CartView, promo *storage.PromoCode) (int64, error)
	GetOrder(ctx context.Context, orderID int64) (*storage.Order, error)
	GetUserOrders(ctx context.Context, userID int64) ([]storage.Order, error)
}

// PromoStore is the slice of storage.PromoStore the API consumes.
type PromoStore interface {
	GetPromoByCode(ctx context.Context, code string) (*storage.PromoCode, error)
	HasUserUsedPromo(ctx context.Context, promoID, userID int64) (bool, error)
}

// RatingStore reports aggregate product ratings (storage.ReviewStore).
type RatingStore interface {
	ProductRating(ctx context.Context, productID int64) (avg float64, count int64, err error)
}

// PhotoStore lists extra product photos (storage.ProductPhotoStore).
type PhotoStore interface {
	List(ctx context.Context, productID int64) ([]storage.ProductPhoto, error)
}

// TelegramAPI is the raw Bot API access used for createInvoiceLink
// (tgbotapi v5 has no typed binding for it).
type TelegramAPI interface {
	MakeRequest(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error)
}

// CryptoInvoicer creates CryptoBot invoices (payment.CryptoBotPayment).
type CryptoInvoicer interface {
	Configured() bool
	CreateInvoice(ctx context.Context, orderID int64, amountUSD float64, description string) (*payment.Invoice, error)
}

// FileURLResolver resolves a Telegram file_id to a direct download URL
// (tgbotapi.BotAPI.GetFileDirectURL).
type FileURLResolver interface {
	GetFileDirectURL(fileID string) (string, error)
}

// Localizer is the slice of service.I18nService the API consumes.
type Localizer interface {
	T(lang, key string) string
	Tf(lang, key string, args ...any) string
	Dict(lang string) map[string]string
}

// Deps carries every dependency of the Mini App API server.
type Deps struct {
	Auth    *Authenticator
	Catalog CatalogService
	Cart    CartService
	Orders  OrderService
	Users   storage.UserStore
	Promos  PromoStore
	Reviews RatingStore
	Photos  PhotoStore
	I18n    Localizer
	Tg      TelegramAPI
	Crypto  CryptoInvoicer
	Files   FileURLResolver
}

type cachedFileURL struct {
	url     string
	expires time.Time
}

// Server is the Mini App REST API. All error bodies are {"error":"<i18n key>"};
// the client translates keys via GET /api/i18n.
type Server struct {
	deps   Deps
	logger *slog.Logger

	httpClient *http.Client

	mu       sync.Mutex
	fileURLs map[string]cachedFileURL
	nowFn    func() time.Time // test seam for the file URL cache
}

// New creates the API server. A nil logger falls back to slog.Default().
func New(deps Deps, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		deps:       deps,
		logger:     logger,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		fileURLs:   make(map[string]cachedFileURL),
		nowFn:      time.Now,
	}
}

// Handler returns the /api/ router, ready to mount on the root mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/i18n", s.handleI18n)
	mux.HandleFunc("GET /api/me", s.withAuth(s.handleMe))
	mux.HandleFunc("GET /api/catalog", s.withAuth(s.handleCatalog))
	mux.HandleFunc("GET /api/products", s.withAuth(s.handleProducts))
	mux.HandleFunc("GET /api/products/{id}", s.withAuth(s.handleProduct))
	mux.HandleFunc("GET /api/cart", s.withAuth(s.handleCartGet))
	mux.HandleFunc("POST /api/cart", s.withAuth(s.handleCartPost))
	mux.HandleFunc("DELETE /api/cart", s.withAuth(s.handleCartDelete))
	mux.HandleFunc("POST /api/checkout", s.withAuth(s.handleCheckout))
	mux.HandleFunc("GET /api/photo/{file_id}", s.withAuth(s.handlePhoto))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, http.StatusNotFound, "webapp_err_not_found")
	})
	return mux
}

// withAuth validates `Authorization: tma <initData>` and passes the result on.
func (s *Server) withAuth(next func(http.ResponseWriter, *http.Request, *AuthResult)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, err := s.deps.Auth.ValidateHeader(r.Header.Get("Authorization"))
		if err != nil {
			s.writeError(w, http.StatusUnauthorized, "webapp_err_unauthorized")
			return
		}
		next(w, r, auth)
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("webapi: encode response", "error", err)
	}
}

// writeError emits {"error":"<i18n key>"} — the contract for every failure.
func (s *Server) writeError(w http.ResponseWriter, status int, key string) {
	s.writeJSON(w, status, map[string]string{"error": key})
}

// GET /api/i18n?lang= — full translation dictionary for the client.
func (s *Server) handleI18n(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.deps.I18n.Dict(r.URL.Query().Get("lang")))
}

// GET /api/me — profile, language, loyalty points.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, auth *AuthResult) {
	u := &storage.User{
		TelegramID:   auth.User.ID,
		Username:     auth.User.Username,
		FirstName:    auth.User.FirstName,
		LanguageCode: auth.User.LanguageCode,
		IsPremium:    auth.User.IsPremium,
	}
	if err := s.deps.Users.Upsert(r.Context(), u); err != nil {
		s.logger.Error("webapi: upsert user", "telegram_id", auth.User.ID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}
	lang := u.LanguageCode
	if lang == "" {
		lang = auth.User.LanguageCode
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"telegram_id":    u.TelegramID,
		"first_name":     u.FirstName,
		"username":       u.Username,
		"language":       lang,
		"loyalty_points": u.LoyaltyPts,
		"loyalty_level":  u.LoyaltyLevel,
	})
}

// GET /api/catalog — active categories.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request, _ *AuthResult) {
	cats, err := s.deps.Catalog.ListCategories(r.Context())
	if err != nil {
		s.logger.Error("webapi: list categories", "error", err)
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}
	out := make([]map[string]any, 0, len(cats))
	for _, c := range cats {
		out = append(out, map[string]any{"id": c.ID, "name": c.Name, "emoji": c.Emoji})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"categories": out})
}

// productJSON is the wire form of a product in lists and cards.
type productJSON struct {
	ID            int64   `json:"id"`
	CategoryID    int64   `json:"category_id"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	Photo         string  `json:"photo,omitempty"`
	PriceUSD      float64 `json:"price_usd"`
	PriceStars    int     `json:"price_stars"`
	Stock         int     `json:"stock"`
	SubPeriodDays int     `json:"sub_period_days,omitempty"`
}

func toProductJSON(p *storage.Product) productJSON {
	return productJSON{
		ID:            p.ID,
		CategoryID:    p.CategoryID,
		Name:          p.Name,
		Description:   p.Description,
		Photo:         photoRef(p.PhotoURL),
		PriceUSD:      p.PriceUSD,
		PriceStars:    p.PriceStars,
		Stock:         p.Stock,
		SubPeriodDays: p.SubPeriodDays,
	}
}

// photoRef maps a stored photo reference (either a public URL or a Telegram
// file_id) to something the web client can load.
func photoRef(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "/api/photo/" + url.PathEscape(raw)
}

// GET /api/products?category=&page= — paginated in-stock products.
func (s *Server) handleProducts(w http.ResponseWriter, r *http.Request, _ *AuthResult) {
	q := r.URL.Query()
	categoryID, err := strconv.ParseInt(q.Get("category"), 10, 64)
	if err != nil || categoryID <= 0 {
		s.writeError(w, http.StatusBadRequest, "webapp_err_bad_request")
		return
	}
	page := 1
	if raw := q.Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 {
			s.writeError(w, http.StatusBadRequest, "webapp_err_bad_request")
			return
		}
	}

	prods, total, err := s.deps.Catalog.ListProductsPaged(r.Context(), categoryID, productsPerPage, (page-1)*productsPerPage)
	if err != nil {
		s.logger.Error("webapi: list products", "category_id", categoryID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}
	out := make([]productJSON, 0, len(prods))
	for i := range prods {
		out = append(out, toProductJSON(&prods[i]))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"products": out,
		"total":    total,
		"page":     page,
		"per_page": productsPerPage,
	})
}

// GET /api/products/{id} — product card with rating and photo gallery.
func (s *Server) handleProduct(w http.ResponseWriter, r *http.Request, _ *AuthResult) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, http.StatusBadRequest, "webapp_err_bad_request")
		return
	}
	ctx := r.Context()

	p, err := s.deps.Catalog.GetProduct(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "webapp_err_not_found")
			return
		}
		s.logger.Error("webapi: get product", "product_id", id, "error", err)
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}

	// Rating and gallery are best-effort decoration: their failure must not
	// take the product card down.
	avg, count, err := s.deps.Reviews.ProductRating(ctx, id)
	if err != nil {
		s.logger.Warn("webapi: product rating", "product_id", id, "error", err)
	}
	photos := make([]string, 0, 4)
	if cover := photoRef(p.PhotoURL); cover != "" {
		photos = append(photos, cover)
	}
	if extra, err := s.deps.Photos.List(ctx, id); err != nil {
		s.logger.Warn("webapi: product photos", "product_id", id, "error", err)
	} else {
		for _, ph := range extra {
			photos = append(photos, photoRef(ph.FileID))
		}
	}

	resp := map[string]any{
		"product":      toProductJSON(p),
		"rating_avg":   avg,
		"rating_count": count,
		"photos":       photos,
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// cartJSON renders a CartView.
func cartJSON(view *shop.CartView) map[string]any {
	items := make([]map[string]any, 0, len(view.Items))
	for _, it := range view.Items {
		items = append(items, map[string]any{
			"product_id":  it.Product.ID,
			"name":        it.Product.Name,
			"photo":       photoRef(it.Product.PhotoURL),
			"price_usd":   it.Product.PriceUSD,
			"price_stars": it.Product.PriceStars,
			"quantity":    it.Quantity,
		})
	}
	return map[string]any{
		"items":       items,
		"total_usd":   view.TotalUSD,
		"total_stars": view.TotalStars,
	}
}

func (s *Server) respondCart(w http.ResponseWriter, r *http.Request, userID int64) {
	view, err := s.deps.Cart.Get(r.Context(), userID)
	if err != nil {
		s.logger.Error("webapi: get cart", "user_id", userID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}
	s.writeJSON(w, http.StatusOK, cartJSON(view))
}

// GET /api/cart — items and totals.
func (s *Server) handleCartGet(w http.ResponseWriter, r *http.Request, auth *AuthResult) {
	s.respondCart(w, r, auth.User.ID)
}

// POST /api/cart {"product_id":N,"delta":M} — delta defaults to 1; a negative
// delta decrements and removes the position at zero. Responds with the updated cart.
func (s *Server) handleCartPost(w http.ResponseWriter, r *http.Request, auth *AuthResult) {
	var req struct {
		ProductID int64 `json:"product_id"`
		Delta     *int  `json:"delta"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}
	delta := 1
	if req.Delta != nil {
		delta = *req.Delta
	}
	if req.ProductID <= 0 || delta == 0 {
		s.writeError(w, http.StatusBadRequest, "webapp_err_bad_request")
		return
	}

	if err := s.deps.Cart.ChangeQuantity(r.Context(), auth.User.ID, req.ProductID, delta); err != nil {
		switch {
		case errors.Is(err, storage.ErrProductOutOfStock):
			s.writeError(w, http.StatusConflict, "webapp_err_out_of_stock")
		case errors.Is(err, storage.ErrNotFound):
			s.writeError(w, http.StatusNotFound, "webapp_err_not_found")
		default:
			s.logger.Error("webapi: change cart quantity", "user_id", auth.User.ID, "product_id", req.ProductID, "error", err)
			s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		}
		return
	}
	s.respondCart(w, r, auth.User.ID)
}

// DELETE /api/cart?product_id=N — remove one position; without product_id the
// whole cart is cleared. Responds with the updated cart.
func (s *Server) handleCartDelete(w http.ResponseWriter, r *http.Request, auth *AuthResult) {
	raw := r.URL.Query().Get("product_id")
	if raw == "" {
		if err := s.deps.Cart.Clear(r.Context(), auth.User.ID); err != nil {
			s.logger.Error("webapi: clear cart", "user_id", auth.User.ID, "error", err)
			s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
			return
		}
		s.respondCart(w, r, auth.User.ID)
		return
	}
	productID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || productID <= 0 {
		s.writeError(w, http.StatusBadRequest, "webapp_err_bad_request")
		return
	}
	if err := s.deps.Cart.Remove(r.Context(), auth.User.ID, productID); err != nil {
		s.logger.Error("webapi: remove cart item", "user_id", auth.User.ID, "product_id", productID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}
	s.respondCart(w, r, auth.User.ID)
}

// decodeBody reads a 64KB-capped JSON body into v, answering the error itself.
func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		s.writeError(w, http.StatusBadRequest, "webapp_err_bad_request")
		return false
	}
	return true
}

// POST /api/checkout {"method":"stars"|"crypto","promo":""} → {"order_id","invoice_link"}.
// The order is created through the same OrderService.CreateFromCart as the bot
// flow; payment confirmation then arrives via the existing successful_payment /
// CryptoBot webhook pipeline.
func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request, auth *AuthResult) {
	var req struct {
		Method string `json:"method"`
		Promo  string `json:"promo"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}
	if req.Method != storage.PaymentMethodStars && req.Method != storage.PaymentMethodCrypto {
		s.writeError(w, http.StatusBadRequest, "webapp_err_method")
		return
	}
	if req.Method == storage.PaymentMethodCrypto && (s.deps.Crypto == nil || !s.deps.Crypto.Configured()) {
		s.writeError(w, http.StatusBadRequest, "webapp_err_crypto_disabled")
		return
	}

	ctx := r.Context()
	userID := auth.User.ID

	view, err := s.deps.Cart.Get(ctx, userID)
	if err != nil {
		s.logger.Error("webapi: get cart for checkout", "user_id", userID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}
	if len(view.Items) == 0 {
		s.writeError(w, http.StatusBadRequest, "webapp_err_empty_cart")
		return
	}
	if err := shop.ValidateSubscriptionCart(view); err != nil {
		s.writeError(w, http.StatusBadRequest, "webapp_err_sub_alone")
		return
	}

	// Subscription products are payable only with Stars and must be ordered
	// alone: Telegram subscription invoices carry exactly one price that
	// recurs every period.
	subPeriod := 0
	for _, it := range view.Items {
		if it.Product.SubPeriodDays > 0 {
			if req.Method != storage.PaymentMethodStars {
				s.writeError(w, http.StatusBadRequest, "webapp_err_sub_stars_only")
				return
			}
			subPeriod = subscriptionPeriodSeconds
		}
	}

	promo, errKey := s.resolvePromo(ctx, userID, req.Promo, view)
	if errKey != "" {
		s.writeError(w, http.StatusBadRequest, errKey)
		return
	}

	orderID, err := s.deps.Orders.CreateFromCart(ctx, userID, view, promo)
	if err != nil {
		var stockErr *shop.ErrInsufficientStock
		switch {
		case errors.As(err, &stockErr):
			s.writeError(w, http.StatusConflict, "webapp_err_out_of_stock")
		case errors.Is(err, storage.ErrEmptyCart):
			s.writeError(w, http.StatusBadRequest, "webapp_err_empty_cart")
		case errors.Is(err, storage.ErrSubscriptionOrderConflict):
			s.writeError(w, http.StatusConflict, "webapp_err_sub_active")
		default:
			s.logger.Error("webapi: create order", "user_id", userID, "error", err)
			s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		}
		return
	}

	order, err := s.deps.Orders.GetOrder(ctx, orderID)
	if err != nil {
		s.logger.Error("webapi: load created order", "order_id", orderID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}

	lang := auth.User.LanguageCode
	var link string
	switch req.Method {
	case storage.PaymentMethodStars:
		link, err = s.createStarsInvoiceLink(lang, order, subPeriod)
	case storage.PaymentMethodCrypto:
		var inv *payment.Invoice
		inv, err = s.deps.Crypto.CreateInvoice(ctx, order.ID, order.TotalUSD, orderDescription(order.Items))
		if err == nil {
			link = inv.PayURL
		}
	}
	if err != nil {
		s.logger.Error("webapi: create invoice link", "order_id", orderID, "method", req.Method, "error", err)
		s.writeError(w, http.StatusBadGateway, "webapp_err_internal")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"order_id":     orderID,
		"invoice_link": link,
	})
}

// resolvePromo validates a promo code for checkout, mirroring the bot flow.
// Returns the promo (nil when code is empty) or the i18n key of the rejection.
func (s *Server) resolvePromo(ctx context.Context, userID int64, code string, view *shop.CartView) (*storage.PromoCode, string) {
	code = strings.TrimSpace(strings.ToUpper(code))
	if code == "" {
		return nil, ""
	}

	promo, err := s.deps.Promos.GetPromoByCode(ctx, code)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, "promo_not_found"
		}
		s.logger.Error("webapi: get promo", "code", code, "error", err)
		return nil, "webapp_err_internal"
	}
	// Personal promos are invisible to anyone but their owner.
	if promo.BoundUserID != nil && *promo.BoundUserID != userID {
		return nil, "promo_not_found"
	}

	used, err := s.deps.Promos.HasUserUsedPromo(ctx, promo.ID, userID)
	if err != nil {
		s.logger.Error("webapi: check promo usage", "code", code, "error", err)
		return nil, "webapp_err_internal"
	}
	if used {
		return nil, "promo_already_used"
	}

	orders, err := s.deps.Orders.GetUserOrders(ctx, userID)
	if err != nil {
		s.logger.Error("webapi: get user orders for promo", "error", err)
		return nil, "webapp_err_internal"
	}
	for _, o := range orders {
		if o.Status == storage.OrderStatusPending && o.PromoCode == promo.Code {
			return nil, "promo_pending_order"
		}
	}

	if promo.CategoryID != nil {
		match := false
		for _, it := range view.Items {
			if it.Product.CategoryID == *promo.CategoryID {
				match = true
				break
			}
		}
		if !match {
			return nil, "promo_category_mismatch"
		}
	}

	return promo, ""
}

// createStarsInvoiceLink calls the raw createInvoiceLink Bot API method
// (tgbotapi v5 has no binding). subPeriod > 0 marks a recurring subscription.
func (s *Server) createStarsInvoiceLink(lang string, order *storage.Order, subPeriod int) (string, error) {
	prices, err := json.Marshal([]tgbotapi.LabeledPrice{
		{Label: s.deps.I18n.T(lang, "webapp_total"), Amount: order.TotalStars},
	})
	if err != nil {
		return "", fmt.Errorf("webapi: marshal prices: %w", err)
	}

	params := tgbotapi.Params{
		"title":       s.deps.I18n.Tf(lang, "webapp_invoice_title", order.ID),
		"description": orderDescription(order.Items),
		// Same payload shape the bot's sendInvoice uses, so successful_payment
		// correlates through the existing handler.
		"payload":  strconv.FormatInt(order.ID, 10),
		"currency": "XTR",
		"prices":   string(prices),
	}
	if subPeriod > 0 {
		params["subscription_period"] = strconv.Itoa(subPeriod)
	}

	resp, err := s.deps.Tg.MakeRequest("createInvoiceLink", params)
	if err != nil {
		return "", fmt.Errorf("webapi: createInvoiceLink: %w", err)
	}
	var link string
	if err := json.Unmarshal(resp.Result, &link); err != nil {
		return "", fmt.Errorf("webapi: parse createInvoiceLink result: %w", err)
	}
	return link, nil
}

// orderDescription builds a short invoice description from order line items.
func orderDescription(items []storage.OrderItem) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		name := it.ProductName
		if name == "" {
			name = fmt.Sprintf("#%d", it.ProductID)
		}
		fmt.Fprintf(&b, "%s × %d", name, it.Quantity)
	}
	const maxLen = 255 // Telegram invoice description limit
	desc := b.String()
	if len(desc) > maxLen {
		desc = desc[:maxLen]
	}
	return desc
}

// GET /api/photo/{file_id} — proxies the Telegram getFile download so the bot
// token never reaches the client. Resolved URLs are cached for fileURLTTL.
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request, _ *AuthResult) {
	fileID := r.PathValue("file_id")
	if fileID == "" {
		s.writeError(w, http.StatusBadRequest, "webapp_err_bad_request")
		return
	}

	fileURL, err := s.resolveFileURL(fileID)
	if err != nil {
		s.logger.Warn("webapi: resolve file", "file_id", fileID, "error", err)
		s.writeError(w, http.StatusNotFound, "webapp_err_not_found")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, fileURL, nil)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "webapp_err_internal")
		return
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		// net/http errors include the request URL; Telegram download URLs
		// embed BOT_TOKEN. Keep the provider URL out of application logs.
		s.logger.Warn("webapi: fetch file", "file_id", fileID, "error", "Telegram file download failed")
		s.writeError(w, http.StatusBadGateway, "webapp_err_internal")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.writeError(w, http.StatusNotFound, "webapp_err_not_found")
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, resp.Body); err != nil {
		s.logger.Warn("webapi: stream file", "file_id", fileID, "error", err)
	}
}

// resolveFileURL returns the direct download URL for a file_id, caching
// results briefly (Telegram file URLs stay valid for at least an hour).
func (s *Server) resolveFileURL(fileID string) (string, error) {
	now := s.nowFn()

	s.mu.Lock()
	if c, ok := s.fileURLs[fileID]; ok && now.Before(c.expires) {
		s.mu.Unlock()
		return c.url, nil
	}
	s.mu.Unlock()

	fileURL, err := s.deps.Files.GetFileDirectURL(fileID)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	// Lazily evict stale entries so the map cannot grow unboundedly.
	for k, c := range s.fileURLs {
		if !now.Before(c.expires) {
			delete(s.fileURLs, k)
		}
	}
	s.fileURLs[fileID] = cachedFileURL{url: fileURL, expires: now.Add(fileURLTTL)}
	s.mu.Unlock()

	return fileURL, nil
}
