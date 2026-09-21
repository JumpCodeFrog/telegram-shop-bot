package bot

// Admin provider status (/paystatus) tests. The command renders one line per
// payment rail (ON/OFF plus the operationally relevant detail) and the
// provider webhook URLs to register. Config matrices are driven through
// newE2EEnvWithConfig; assertions target the recorded sendMessage text.
// The command must render presence booleans, rates and URLs only — never
// secret values — and stay fully inert for non-admins.

import (
	"strings"
	"testing"

	"shop_bot/internal/config"
)

// payStatusText drives /paystatus as the admin and returns the single
// sendMessage text.
func payStatusText(t *testing.T, e *e2eEnv) string {
	t.Helper()
	calls := e.cmd(e2eAdminID, "/paystatus", "en")
	if len(calls) != 1 || calls[0].Method != "sendMessage" {
		t.Fatalf("/paystatus calls=%+v", calls)
	}
	return calls[0].Params.Get("text")
}

func TestPayStatusAllUnconfigured(t *testing.T) {
	// The default e2e config carries a CryptoBot token; clear it so every
	// credential-gated rail is off.
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		c.CryptoBotToken = ""
	})

	text := payStatusText(t, e)
	for _, want := range []string{
		e.bot.t("en", "admin_paystatus_stars"),
		e.bot.t("en", "admin_paystatus_crypto_off"),
		e.bot.t("en", "admin_paystatus_yookassa_off"),
		e.bot.t("en", "admin_paystatus_stripe_off"),
		e.bot.t("en", "admin_paystatus_ton_off"),
		e.bot.t("en", "admin_paystatus_nowpayments_off"),
		e.bot.t("en", "admin_paystatus_balance"),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("paystatus missing %q:\n%s", want, text)
		}
	}
	// No credential-gated rail is configured: no webhook section at all.
	if strings.Contains(text, e.bot.t("en", "admin_paystatus_webhooks_title")) {
		t.Errorf("unconfigured status must hide the webhook section:\n%s", text)
	}
}

// TestPayStatusWhitespaceOnlyCredentialsRenderOff pins the TrimSpace
// unification (HANDOFF §6.10): a whitespace-only credential is a
// misconfiguration, not a configured rail — /paystatus must render OFF for it
// exactly like doctor diagnoses it, never a misleading ON/WARN.
func TestPayStatusWhitespaceOnlyCredentialsRenderOff(t *testing.T) {
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		c.CryptoBotToken = " "
		c.YooKassaShopID, c.YooKassaSecretKey, c.YooKassaReturnURL = " ", " ", " "
		c.StripeSecretKey, c.StripeWebhookSecret, c.StripeReturnURL = " ", " ", " "
		c.TONWalletAddress, c.TONAPIKey = " ", " "
		c.NowpaymentsAPIKey, c.NowpaymentsIPNSecret, c.NowpaymentsReturnURL = " ", " ", " "
	})

	text := payStatusText(t, e)
	for _, want := range []string{
		e.bot.t("en", "admin_paystatus_crypto_off"),
		e.bot.t("en", "admin_paystatus_yookassa_off"),
		e.bot.t("en", "admin_paystatus_stripe_off"),
		e.bot.t("en", "admin_paystatus_ton_off"),
		e.bot.t("en", "admin_paystatus_nowpayments_off"),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("paystatus missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, e.bot.t("en", "admin_paystatus_webhooks_title")) {
		t.Errorf("whitespace-only credentials must not open the webhook section:\n%s", text)
	}
}

func TestPayStatusFullyConfigured(t *testing.T) {
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		enableYooKassa(c)
		enableStripe(c)
		enableTON(c)
		c.TONAPIKey = "ton-api-key-secret"
		enableNowpayments(c)
		// Trailing slash must be trimmed by the webhook URL helpers.
		c.WebhookURL = "https://shop.example.com/"
	})

	text := payStatusText(t, e)
	for _, want := range []string{
		e.bot.t("en", "admin_paystatus_stars"),
		e.bot.t("en", "admin_paystatus_crypto_on"),
		e.bot.t("en", "admin_paystatus_yookassa_on"),
		e.bot.t("en", "admin_paystatus_stripe_on"),
		e.bot.t("en", "admin_paystatus_ton_on"),
		e.bot.t("en", "admin_paystatus_nowpayments_on"),
		e.bot.t("en", "admin_paystatus_balance"),
	} {
		line := want
		if strings.Count(want, "%s") > 0 {
			line = strings.Split(want, "%s")[0]
		}
		if !strings.Contains(text, line) {
			t.Errorf("paystatus missing %q:\n%s", line, text)
		}
	}
	// Rates are shown; the TON line notes the API key.
	for _, want := range []string{"92.5", "5.25", e.bot.t("en", "admin_paystatus_ton_api_key")} {
		if !strings.Contains(text, want) {
			t.Errorf("paystatus missing %q:\n%s", want, text)
		}
	}
	// Every configured rail lists the webhook URL to register; CryptoBot's
	// line notes that the IPN is configured in the CryptoBot app UI.
	for _, want := range []string{
		"https://shop.example.com/cryptobot-webhook",
		"https://shop.example.com/yookassa-webhook",
		"https://shop.example.com/stripe-webhook",
		"https://shop.example.com/nowpayments-webhook",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("paystatus missing webhook URL %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "//cryptobot-webhook") || strings.Contains(text, "//yookassa-webhook") {
		t.Errorf("trailing slash of the base URL must be trimmed:\n%s", text)
	}

	// Zero secrets: no token, key, secret, shop ID or wallet address value
	// may appear in any rendering.
	for _, secret := range []string{
		e2eCryptoToken,
		"123456", "live_key", "/return",
		"sk_test_e2e", "whsec_e2e",
		tonTestWalletAddress, "ton-api-key-secret",
		"np-api-key", "np-ipn-secret",
	} {
		if strings.Contains(text, secret) {
			t.Errorf("paystatus leaked %q:\n%s", secret, text)
		}
	}
}

func TestPayStatusYooKassaCredsWithoutRate(t *testing.T) {
	// Credentials without USD_TO_RUB_RATE: the WARN state — the rail stays
	// hidden at checkout, but the webhook URL is already worth registering.
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		c.YooKassaShopID = "123456"
		c.YooKassaSecretKey = "live_key"
		c.YooKassaReturnURL = "https://shop.example.com/return"
		c.WebhookURL = "https://shop.example.com"
	})

	text := payStatusText(t, e)
	if !strings.Contains(text, e.bot.t("en", "admin_paystatus_yookassa_warn")) {
		t.Fatalf("paystatus missing the YooKassa WARN line:\n%s", text)
	}
	yookassaOnPrefix := strings.Split(e.bot.t("en", "admin_paystatus_yookassa_on"), "%s")[0]
	if strings.Contains(text, yookassaOnPrefix) ||
		strings.Contains(text, e.bot.t("en", "admin_paystatus_yookassa_off")) {
		t.Fatalf("WARN state must replace ON/OFF:\n%s", text)
	}
	if !strings.Contains(text, "https://shop.example.com/yookassa-webhook") {
		t.Fatalf("configured YooKassa must list its webhook URL even without a rate:\n%s", text)
	}
	if strings.Contains(text, "live_key") {
		t.Fatalf("paystatus leaked the YooKassa secret key:\n%s", text)
	}
}

func TestPayStatusTONAddressWithoutRate(t *testing.T) {
	// Wallet address without USD_PER_TON: the WARN state.
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		c.TONWalletAddress = tonTestWalletAddress
	})

	text := payStatusText(t, e)
	if !strings.Contains(text, e.bot.t("en", "admin_paystatus_ton_warn")) {
		t.Fatalf("paystatus missing the TON WARN line:\n%s", text)
	}
	if strings.Contains(text, tonTestWalletAddress) {
		t.Fatalf("paystatus leaked the wallet address:\n%s", text)
	}
	// TON polls toncenter: no webhook URL is ever listed for it.
	if strings.Contains(text, "ton-webhook") {
		t.Fatalf("TON must not list a webhook URL:\n%s", text)
	}
}

func TestPayStatusTONWithoutAPIKey(t *testing.T) {
	// Address + rate without the optional toncenter API key.
	e := newE2EEnvWithConfig(t, enableTON)

	text := payStatusText(t, e)
	if !strings.Contains(text, "5.25") || !strings.Contains(text, e.bot.t("en", "admin_paystatus_ton_no_api_key")) {
		t.Fatalf("TON line must show the rate and the missing-API-key note:\n%s", text)
	}
}

func TestPayStatusWebhookBaseUnset(t *testing.T) {
	// Rails configured but no public base URL: webhook URLs cannot be
	// derived, so the section carries the note instead.
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		enableStripe(c)
	})

	text := payStatusText(t, e)
	if !strings.Contains(text, e.bot.t("en", "admin_paystatus_webhook_no_base")) {
		t.Fatalf("paystatus missing the WEBHOOK_URL note:\n%s", text)
	}
	if strings.Contains(text, "cryptobot-webhook") || strings.Contains(text, "stripe-webhook") {
		t.Fatalf("no webhook URLs may be rendered without a base URL:\n%s", text)
	}
}

func TestPayStatusNonAdminInert(t *testing.T) {
	e := newE2EEnv(t)

	if calls := e.cmd(1234, "/paystatus", "en"); len(calls) != 0 {
		t.Fatalf("non-admin /paystatus produced calls=%+v", calls)
	}
}
