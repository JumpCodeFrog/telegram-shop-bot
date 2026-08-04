package bot

import (
	"strings"
	"testing"

	"shop_bot/internal/storage"
)

func TestFormatReferralText(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)
	link := "https://t.me/testbot?start=ref_Ab3xYz19"

	got := b.formatReferralText("en", link, 3, 200)
	for _, want := range []string{
		"Referral program",
		"<code>" + link + "</code>",
		"Friends invited: <b>3</b>",
		"Points earned: <b>200</b>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("en referral text missing %q:\n%q", want, got)
		}
	}

	gotRU := b.formatReferralText("ru", link, 0, 0)
	for _, want := range []string{
		"Реферальная программа",
		"Приглашено друзей: <b>0</b>",
		"Начислено баллов: <b>0</b>",
	} {
		if !strings.Contains(gotRU, want) {
			t.Fatalf("ru referral text missing %q:\n%q", want, gotRU)
		}
	}
}

func TestReferralLink_UsesRefPrefix(t *testing.T) {
	t.Parallel()

	// nil api → empty username; the deep-link shape must still hold.
	var b *Bot = &Bot{}
	if got, want := b.referralLink("Ab3xYz19"), "https://t.me/?start=ref_Ab3xYz19"; got != want {
		t.Fatalf("referralLink = %q, want %q", got, want)
	}
}

func TestLoyaltyNextLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level     string
		next      string
		threshold int
	}{
		{"bronze", "silver", 1000},
		{"silver", "gold", 5000},
		{"gold", "vip", 10000},
		{"vip", "", 0},
		{"", "", 0},
	}
	for _, tc := range tests {
		next, threshold := loyaltyNextLevel(tc.level)
		if next != tc.next || threshold != tc.threshold {
			t.Errorf("loyaltyNextLevel(%q) = (%q, %d), want (%q, %d)", tc.level, next, threshold, tc.next, tc.threshold)
		}
	}
}

func TestFormatProfileText_LoyaltyProgress(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)

	bronze := &storage.User{FirstName: "Ann", LoyaltyLevel: "bronze", LoyaltyPts: 250}
	got := b.formatProfileText("en", bronze, 1)
	if !strings.Contains(got, "<b>silver</b>") || !strings.Contains(got, "250/1000") {
		t.Fatalf("bronze profile missing progress to silver (250/1000):\n%q", got)
	}

	vip := &storage.User{FirstName: "Ann", LoyaltyLevel: "vip", LoyaltyPts: 20000}
	gotVip := b.formatProfileText("en", vip, 1)
	if !strings.Contains(gotVip, "Maximum level reached") {
		t.Fatalf("vip profile missing max-level line:\n%q", gotVip)
	}
	if strings.Contains(gotVip, "20000/") {
		t.Fatalf("vip profile must not show progress fraction:\n%q", gotVip)
	}
}
