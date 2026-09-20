package config

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// isHTTPSURL reports whether raw is a usable public HTTPS URL: a parseable
// URL whose scheme is https (case-insensitive per RFC 3986, so an uppercase
// HTTPS:// is accepted) and whose host is non-empty (a bare https:// is
// not). Every provider return-URL check goes through this one predicate.
func isHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.ToLower(u.Scheme) == "https" && u.Host != ""
}

const maxTelegramUserID int64 = (1 << 52) - 1

const (
	minTelegramWebhookSecretBytes = 32
	maxTelegramWebhookSecretBytes = 256
)

// ValidateBotToken rejects empty/example/path-like values without freezing a
// future BotFather secret length or alphabet. Telegram getMe is authoritative.
func ValidateBotToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("is required but not set")
	}
	lower := strings.ToLower(token)
	if strings.Contains(lower, "your_token") || strings.Contains(lower, "xxxxxxxx") {
		return errors.New("still contains an example placeholder")
	}
	if strings.ContainsAny(token, "/\\\r\n\t ") {
		return errors.New("must be a single BotFather token without whitespace or path separators")
	}
	prefix, secret, ok := strings.Cut(token, ":")
	botID, err := strconv.ParseInt(prefix, 10, 64)
	if !ok || secret == "" || err != nil || botID <= 0 {
		return errors.New("must contain a positive bot ID, a colon, and a non-empty secret")
	}
	return nil
}

// ValidateAdminUserID accepts Telegram user IDs, not group or channel IDs.
func ValidateAdminUserID(id int64) error {
	if id <= 0 || id > maxTelegramUserID {
		return errors.New("must contain positive Telegram user IDs")
	}
	return nil
}

// ValidateTelegramWebhookSecret enforces a high-entropy-compatible Telegram
// secret_token. Telegram accepts A-Z, a-z, 0-9, underscore and hyphen; a
// 32-character minimum gives operators enough room for at least 128 bits of
// randomness while keeping the value compatible with the Bot API.
func ValidateTelegramWebhookSecret(secret string) error {
	if secret == "" {
		return errors.New("is required")
	}
	if len(secret) < minTelegramWebhookSecretBytes {
		return errors.New("must be at least 32 characters")
	}
	if len(secret) > maxTelegramWebhookSecretBytes {
		return errors.New("must be at most 256 characters")
	}
	for _, ch := range secret {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			continue
		}
		return errors.New("may contain only A-Z, a-z, 0-9, underscore and hyphen")
	}
	return nil
}

// TelegramWebhookURL turns the configured public base URL into the single
// Telegram callback URL used by runtime checks, registration, and smoke tools.
func TelegramWebhookURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	return base + "/telegram-webhook"
}

// ValidateYooKassaConfig enforces all-or-none credentials and an HTTPS return URL.
func ValidateYooKassaConfig(shopID, secretKey, returnURL string) error {
	set := 0
	for _, v := range []string{shopID, secretKey, returnURL} {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		return nil
	}
	if set != 3 {
		return errors.New("YOOKASSA_SHOP_ID, YOOKASSA_SECRET_KEY and YOOKASSA_RETURN_URL must be set together")
	}
	if !isHTTPSURL(returnURL) {
		return errors.New("YOOKASSA_RETURN_URL must be a public https:// URL")
	}
	return nil
}

// YooKassaWebhookURL turns the configured public base URL into the YooKassa
// notification endpoint, mirroring TelegramWebhookURL.
func YooKassaWebhookURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	return base + "/yookassa-webhook"
}

// ValidateStripeConfig enforces all-or-none credentials, the Stripe key
// prefixes, and an HTTPS return URL.
func ValidateStripeConfig(secretKey, webhookSecret, returnURL string) error {
	set := 0
	for _, v := range []string{secretKey, webhookSecret, returnURL} {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		return nil
	}
	if set != 3 {
		return errors.New("STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET and STRIPE_RETURN_URL must be set together")
	}
	if !isHTTPSURL(returnURL) {
		return errors.New("STRIPE_RETURN_URL must be a public https:// URL")
	}
	if !strings.HasPrefix(secretKey, "sk_live_") && !strings.HasPrefix(secretKey, "sk_test_") {
		return errors.New("STRIPE_SECRET_KEY must start with sk_live_ or sk_test_")
	}
	if !strings.HasPrefix(webhookSecret, "whsec_") {
		return errors.New("STRIPE_WEBHOOK_SECRET must start with whsec_")
	}
	return nil
}

// StripeWebhookURL turns the configured public base URL into the Stripe
// webhook endpoint, mirroring YooKassaWebhookURL.
func StripeWebhookURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	return base + "/stripe-webhook"
}

// tonFriendlyAddressLength is the length of a base64url-encoded TON friendly
// address (36 bytes: workchain flag + workchain + 256-bit hash + CRC16).
const tonFriendlyAddressLength = 48

// ValidateTONConfig enforces the TON polling-provider combination: a wallet
// address requires a positive USD_PER_TON rate, the rate requires the
// address, and the optional toncenter API key is valid only alongside both.
func ValidateTONConfig(address string, usdPerTON float64, apiKey string) error {
	if address == "" {
		if usdPerTON > 0 {
			return errors.New("USD_PER_TON requires TON_WALLET_ADDRESS to be set")
		}
		if apiKey != "" {
			return errors.New("TON_API_KEY requires TON_WALLET_ADDRESS and USD_PER_TON to be set")
		}
		return nil
	}
	if usdPerTON <= 0 {
		return errors.New("USD_PER_TON must be set and positive when TON_WALLET_ADDRESS is set")
	}
	// Shape check only: a well-formed-but-wrong address fails at toncenter
	// polling time, not at boot — full validation is toncenter's job.
	if len(address) != tonFriendlyAddressLength {
		return errors.New("TON_WALLET_ADDRESS must be a 48-character base64url friendly address")
	}
	for _, ch := range address {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			continue
		}
		return errors.New("TON_WALLET_ADDRESS must be base64url (A-Z, a-z, 0-9, underscore and hyphen only)")
	}
	return nil
}

// ValidateNowpaymentsConfig enforces all-or-none credentials and an HTTPS
// return URL, mirroring the Stripe validator. NOWPayments documents no key
// or secret prefixes, so none are enforced.
func ValidateNowpaymentsConfig(apiKey, ipnSecret, returnURL string) error {
	set := 0
	for _, v := range []string{apiKey, ipnSecret, returnURL} {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		return nil
	}
	if set != 3 {
		return errors.New("NOWPAYMENTS_API_KEY, NOWPAYMENTS_IPN_SECRET and NOWPAYMENTS_RETURN_URL must be set together")
	}
	if !isHTTPSURL(returnURL) {
		return errors.New("NOWPAYMENTS_RETURN_URL must be a public https:// URL")
	}
	return nil
}

// NowpaymentsWebhookURL turns the configured public base URL into the
// NOWPayments IPN endpoint, mirroring StripeWebhookURL.
func NowpaymentsWebhookURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	return base + "/nowpayments-webhook"
}
