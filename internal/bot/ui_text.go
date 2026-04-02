package bot

import (
	"fmt"
	"html"
	"strings"

	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

func escapeHTML(s string) string {
	return html.EscapeString(s)
}

func trimPreview(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return ""
	}

	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}

	return strings.TrimSpace(string(runes[:maxRunes])) + "..."
}

func (b *Bot) productStockText(lang string, stock int) string {
	if stock <= 0 {
		return b.t(lang, "product_out_of_stock")
	}
	return fmt.Sprintf(b.t(lang, "product_in_stock"), stock)
}

func (b *Bot) orderStatusText(lang, status string) string {
	switch status {
	case storage.OrderStatusPending:
		return b.t(lang, "order_status_pending")
	case storage.OrderStatusPaid:
		return b.t(lang, "order_status_paid")
	case storage.OrderStatusDelivered:
		return b.t(lang, "order_status_delivered")
	case storage.OrderStatusCancelled:
		return b.t(lang, "order_status_cancelled")
	default:
		return escapeHTML(status)
	}
}

func (b *Bot) paymentMethodText(lang, method string) string {
	switch method {
	case storage.PaymentMethodStars:
		return b.t(lang, "payment_method_stars")
	case storage.PaymentMethodCrypto:
		return b.t(lang, "payment_method_crypto")
	case storage.PaymentMethodBalance:
		return b.t(lang, "payment_method_balance")
	default:
		return escapeHTML(method)
	}
}

func (b *Bot) formatProductText(lang string, p *storage.Product) string {
	return fmt.Sprintf(
		b.t(lang, "product_card_text"),
		escapeHTML(p.Name),
		escapeHTML(strings.TrimSpace(p.Description)),
		p.PriceUSD,
		p.PriceStars,
		b.productStockText(lang, p.Stock),
	)
}

func (b *Bot) formatCategoryProductsText(lang string, category *storage.Category, products []storage.Product, page, totalPages int, wishlistIDs map[int64]struct{}) string {
	var sb strings.Builder

	titleText := b.t(lang, "catalog")
	if category != nil {
		titleText = strings.TrimSpace(strings.TrimSpace(category.Emoji + " " + category.Name))
	}
	title := escapeHTML(titleText)
	sb.WriteString(fmt.Sprintf(b.t(lang, "products_title"), title))

	for i, p := range products {
		name := escapeHTML(p.Name)
		if _, ok := wishlistIDs[p.ID]; ok {
			name = "❤️ " + name
		}

		desc := escapeHTML(trimPreview(p.Description, 42))
		if desc == "" {
			desc = escapeHTML(b.t(lang, "product_list_no_description"))
		}

		sb.WriteString(fmt.Sprintf(
			b.t(lang, "product_list_item"),
			page*productsPerPage+i+1,
			name,
			desc,
			p.PriceUSD,
			p.PriceStars,
			b.productStockText(lang, p.Stock),
		))
	}

	if totalPages > 1 {
		sb.WriteString(fmt.Sprintf(b.t(lang, "product_list_page"), page+1, totalPages))
	}

	return sb.String()
}

func (b *Bot) formatCartText(lang string, view *shop.CartView) string {
	var sb strings.Builder
	sb.WriteString(b.t(lang, "cart_title"))
	for _, item := range view.Items {
		sb.WriteString(fmt.Sprintf(
			b.t(lang, "cart_item_line"),
			escapeHTML(item.Product.Name),
			item.Quantity,
			item.Product.PriceUSD*float64(item.Quantity),
			item.Product.PriceStars*item.Quantity,
		))
	}
	sb.WriteString(fmt.Sprintf(b.t(lang, "cart_total"), view.TotalUSD, view.TotalStars))
	return sb.String()
}

func (b *Bot) formatCheckoutText(lang string, view *shop.CartView) string {
	var sb strings.Builder
	sb.WriteString(b.t(lang, "checkout_title"))
	sb.WriteString(b.t(lang, "checkout_items_header"))
	for _, item := range view.Items {
		sb.WriteString(fmt.Sprintf(
			b.t(lang, "checkout_item_line"),
			escapeHTML(item.Product.Name),
			item.Quantity,
			item.Product.PriceUSD*float64(item.Quantity),
			item.Product.PriceStars*item.Quantity,
		))
	}
	sb.WriteString(fmt.Sprintf(b.t(lang, "checkout_total"), view.TotalUSD, view.TotalStars))
	return sb.String()
}

func (b *Bot) formatPaymentMethodsText(lang string, orderID int64, view *shop.CartView, cryptoEnabled bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(b.t(lang, "payment_methods_title"), orderID))
	sb.WriteString(b.t(lang, "payment_methods_items_header"))
	for _, item := range view.Items {
		sb.WriteString(fmt.Sprintf(
			b.t(lang, "payment_methods_item_line"),
			escapeHTML(item.Product.Name),
			item.Quantity,
			item.Product.PriceUSD*float64(item.Quantity),
			item.Product.PriceStars*item.Quantity,
		))
	}
	sb.WriteString(fmt.Sprintf(b.t(lang, "payment_methods_total"), view.TotalUSD, view.TotalStars))
	sb.WriteString(b.t(lang, "payment_methods_hint"))
	if !cryptoEnabled {
		sb.WriteString(b.t(lang, "order_created_no_crypto"))
	}
	return sb.String()
}

func (b *Bot) formatOrdersText(lang string, orders []storage.Order) string {
	var sb strings.Builder
	sb.WriteString(b.t(lang, "orders_title"))
	for _, o := range orders {
		sb.WriteString(fmt.Sprintf(b.t(lang, "orders_order_line"), o.ID))
		sb.WriteString(fmt.Sprintf(b.t(lang, "orders_date_line"), o.CreatedAt.Format("02.01.2006 15:04")))
		sb.WriteString(fmt.Sprintf(b.t(lang, "orders_total_line"), o.TotalUSD, o.TotalStars))
		if o.PaymentMethod != "" {
			sb.WriteString(fmt.Sprintf(b.t(lang, "orders_payment_line"), b.paymentMethodText(lang, o.PaymentMethod)))
		}
		if o.PromoCode != "" {
			sb.WriteString(fmt.Sprintf(b.t(lang, "orders_promo_line"), escapeHTML(o.PromoCode), o.DiscountPct))
		}
		sb.WriteString(fmt.Sprintf(b.t(lang, "order_status_line"), b.orderStatusText(lang, o.Status)))
	}
	return sb.String()
}

func (b *Bot) formatProfileText(lang string, user *storage.User, orderCount int) string {
	var sb strings.Builder
	sb.WriteString(b.t(lang, "profile_title"))
	if user == nil {
		sb.WriteString(b.t(lang, "profile_not_init"))
		return sb.String()
	}

	name := user.FirstName
	if name == "" {
		name = user.Username
	}
	if name == "" {
		name = b.t(lang, "profile_default_name")
	}
	if user.IsPremium {
		name = "⭐ " + name
	}

	sb.WriteString(fmt.Sprintf(b.t(lang, "profile_name_line"), escapeHTML(name)))
	if user.Username != "" {
		sb.WriteString(fmt.Sprintf(b.t(lang, "profile_username_line"), escapeHTML(user.Username)))
	}
	sb.WriteString(fmt.Sprintf(b.t(lang, "profile_balance_line"), user.BalanceUSD))
	sb.WriteString(fmt.Sprintf(b.t(lang, "profile_loyalty_line"), escapeHTML(user.LoyaltyLevel), user.LoyaltyPts))
	sb.WriteString(fmt.Sprintf(b.t(lang, "profile_orders_line"), orderCount))
	if user.ReferralCode.Valid && user.ReferralCode.String != "" {
		sb.WriteString(fmt.Sprintf(b.t(lang, "profile_referral_line"), escapeHTML(user.ReferralCode.String)))
	}

	return sb.String()
}

func (b *Bot) formatWishlistText(lang string, products []storage.Product) string {
	var sb strings.Builder
	sb.WriteString(b.t(lang, "wishlist_title"))
	sb.WriteString("\n\n")
	for _, p := range products {
		sb.WriteString(fmt.Sprintf(
			b.t(lang, "wishlist_item_line"),
			escapeHTML(p.Name),
			p.PriceUSD,
		))
	}
	return sb.String()
}

func (b *Bot) productQuantityLabel(lang string, quantity int) string {
	return fmt.Sprintf(b.t(lang, "product_qty_label"), quantity)
}
