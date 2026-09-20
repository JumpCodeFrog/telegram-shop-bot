package bot

import (
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/config"
)

// handlePayStatus renders the per-rail payment provider status for admins.
// Admin-gated; non-admins are silently ignored.
func (b *Bot) handlePayStatus(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.formatPayStatus(msg.From.LanguageCode)))
}

// formatPayStatus renders one line per payment rail — ON/OFF plus the
// operationally relevant detail (exchange rate, optional API key) — followed
// by the provider webhook URLs to register. Presence booleans, rates and URLs
// only: secret values (tokens, keys, secrets, wallet address) are never
// printed.
func (b *Bot) formatPayStatus(lang string) string {
	var (
		yooRate, tonRate          float64
		tonAddress, tonAPIKey     string
		baseURL                   string
		cryptoOn, stripeOn, nowOn bool
		yooCreds                  bool
	)
	if b.cfg != nil {
		yooRate = b.cfg.USDToRUBRate
		tonRate = b.cfg.USDPerTON
		tonAddress = b.cfg.TONWalletAddress
		tonAPIKey = b.cfg.TONAPIKey
		baseURL = b.cfg.WebhookURL
		cryptoOn = strings.TrimSpace(b.cfg.CryptoBotToken) != ""
		yooCreds = b.cfg.YooKassaShopID != "" && b.cfg.YooKassaSecretKey != "" && b.cfg.YooKassaReturnURL != ""
		stripeOn = b.cfg.StripeSecretKey != "" && b.cfg.StripeWebhookSecret != "" && b.cfg.StripeReturnURL != ""
		nowOn = b.cfg.NowpaymentsAPIKey != "" && b.cfg.NowpaymentsIPNSecret != "" && b.cfg.NowpaymentsReturnURL != ""
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_paystatus_title"))

	// Stars and Balance are built-in rails: always available, no credentials.
	sb.WriteString(b.t(lang, "admin_paystatus_stars"))

	if cryptoOn {
		sb.WriteString(b.t(lang, "admin_paystatus_crypto_on"))
	} else {
		sb.WriteString(b.t(lang, "admin_paystatus_crypto_off"))
	}

	switch {
	case yooCreds && yooRate > 0:
		sb.WriteString(fmt.Sprintf(b.t(lang, "admin_paystatus_yookassa_on"), formatPayStatusRate(yooRate)))
	case yooCreds:
		// Credentials without the rate: the RUB button stays hidden at
		// checkout (mirrors yooKassaPaymentsEnabled).
		sb.WriteString(b.t(lang, "admin_paystatus_yookassa_warn"))
	default:
		sb.WriteString(b.t(lang, "admin_paystatus_yookassa_off"))
	}

	if stripeOn {
		sb.WriteString(b.t(lang, "admin_paystatus_stripe_on"))
	} else {
		sb.WriteString(b.t(lang, "admin_paystatus_stripe_off"))
	}

	switch {
	case tonAddress != "" && tonRate > 0:
		note := b.t(lang, "admin_paystatus_ton_no_api_key")
		if tonAPIKey != "" {
			note = b.t(lang, "admin_paystatus_ton_api_key")
		}
		sb.WriteString(fmt.Sprintf(b.t(lang, "admin_paystatus_ton_on"), formatPayStatusRate(tonRate), note))
	case tonAddress != "":
		// Address without the rate: the TON button stays hidden at
		// checkout (mirrors tonPaymentsEnabled).
		sb.WriteString(b.t(lang, "admin_paystatus_ton_warn"))
	default:
		sb.WriteString(b.t(lang, "admin_paystatus_ton_off"))
	}

	if nowOn {
		sb.WriteString(b.t(lang, "admin_paystatus_nowpayments_on"))
	} else {
		sb.WriteString(b.t(lang, "admin_paystatus_nowpayments_off"))
	}

	sb.WriteString(b.t(lang, "admin_paystatus_balance"))

	// Webhook URLs to register: only configured rails need one, and the
	// URLs are derivable only when the public base URL is set. Stars,
	// Balance (Telegram/internal) and TON (toncenter polling) have no
	// provider webhook.
	if cryptoOn || yooCreds || stripeOn || nowOn {
		sb.WriteString(b.t(lang, "admin_paystatus_webhooks_title"))
		if strings.TrimSpace(baseURL) == "" {
			sb.WriteString(b.t(lang, "admin_paystatus_webhook_no_base"))
		} else {
			if cryptoOn {
				// CryptoBot's IPN is registered in the CryptoBot app UI,
				// not via a provider dashboard API.
				sb.WriteString(fmt.Sprintf(b.t(lang, "admin_paystatus_webhook_crypto"), cryptoBotWebhookURL(baseURL)))
			}
			if yooCreds {
				sb.WriteString(fmt.Sprintf(b.t(lang, "admin_paystatus_webhook_yookassa"), config.YooKassaWebhookURL(baseURL)))
			}
			if stripeOn {
				sb.WriteString(fmt.Sprintf(b.t(lang, "admin_paystatus_webhook_stripe"), config.StripeWebhookURL(baseURL)))
			}
			if nowOn {
				sb.WriteString(fmt.Sprintf(b.t(lang, "admin_paystatus_webhook_nowpayments"), config.NowpaymentsWebhookURL(baseURL)))
			}
		}
	}

	return sb.String()
}

// formatPayStatusRate prints an exchange rate in its shortest round-trip
// form so locale templates need only a %s verb.
func formatPayStatusRate(rate float64) string {
	return strconv.FormatFloat(rate, 'g', -1, 64)
}

// cryptoBotWebhookURL derives the CryptoBot IPN endpoint from the public
// base URL, mirroring config.YooKassaWebhookURL; the mounted path lives in
// cmd/bot/http_routes.go.
func cryptoBotWebhookURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	return base + "/cryptobot-webhook"
}
