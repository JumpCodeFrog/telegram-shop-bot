package webapi

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testBotToken = "7000000000:AAH-test-token-for-webapi-auth"

// testInitData builds a correctly signed initData string for testBotToken.
func testInitData(t *testing.T, authDate time.Time) string {
	t.Helper()
	v := url.Values{}
	v.Set("query_id", "AAHdF6IQAAAAAN0XohDhrOrc")
	v.Set("user", `{"id":42,"first_name":"Ann","last_name":"Lee","username":"ann","language_code":"ru","is_premium":true}`)
	v.Set("auth_date", strconv.FormatInt(authDate.Unix(), 10))
	return SignInitData(testBotToken, v)
}

func TestValidateAcceptsSelfSignedVector(t *testing.T) {
	a := NewAuthenticator(testBotToken, DefaultAuthTTL)

	res, err := a.Validate(testInitData(t, time.Now()))
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if res.User.ID != 42 {
		t.Errorf("User.ID = %d, want 42", res.User.ID)
	}
	if res.User.Username != "ann" {
		t.Errorf("User.Username = %q, want %q", res.User.Username, "ann")
	}
	if res.User.LanguageCode != "ru" {
		t.Errorf("User.LanguageCode = %q, want %q", res.User.LanguageCode, "ru")
	}
	if !res.User.IsPremium {
		t.Error("User.IsPremium = false, want true")
	}
}

func TestValidateRejectsTamperedPayload(t *testing.T) {
	a := NewAuthenticator(testBotToken, DefaultAuthTTL)

	initData := testInitData(t, time.Now())
	// Swap the signed user for another one, keeping the original hash.
	tampered := strings.Replace(initData, url.QueryEscape(`"id":42`), url.QueryEscape(`"id":43`), 1)
	if tampered == initData {
		t.Fatal("test setup: payload substitution did not apply")
	}

	if _, err := a.Validate(tampered); !errors.Is(err, ErrBadSignature) {
		t.Errorf("Validate(tampered payload) error = %v, want ErrBadSignature", err)
	}
}

func TestValidateRejectsCorruptedHash(t *testing.T) {
	a := NewAuthenticator(testBotToken, DefaultAuthTTL)

	initData := testInitData(t, time.Now())
	v, err := url.ParseQuery(initData)
	if err != nil {
		t.Fatal(err)
	}
	h := []byte(v.Get("hash"))
	if h[0] == '0' {
		h[0] = '1'
	} else {
		h[0] = '0'
	}
	v.Set("hash", string(h))

	if _, err := a.Validate(v.Encode()); !errors.Is(err, ErrBadSignature) {
		t.Errorf("Validate(corrupted hash) error = %v, want ErrBadSignature", err)
	}
}

func TestValidateRejectsWrongBotToken(t *testing.T) {
	a := NewAuthenticator("other-bot-token", DefaultAuthTTL)

	if _, err := a.Validate(testInitData(t, time.Now())); !errors.Is(err, ErrBadSignature) {
		t.Errorf("Validate(wrong token) error = %v, want ErrBadSignature", err)
	}
}

func TestValidateRejectsMissingHash(t *testing.T) {
	a := NewAuthenticator(testBotToken, DefaultAuthTTL)

	if _, err := a.Validate("auth_date=1&user=%7B%22id%22%3A42%7D"); !errors.Is(err, ErrBadSignature) {
		t.Errorf("Validate(no hash) error = %v, want ErrBadSignature", err)
	}
}

func TestValidateRejectsExpiredAuthDate(t *testing.T) {
	a := NewAuthenticator(testBotToken, DefaultAuthTTL)

	// Just over the 1h TTL — signature itself is valid.
	initData := testInitData(t, time.Now().Add(-DefaultAuthTTL-time.Minute))
	if _, err := a.Validate(initData); !errors.Is(err, ErrExpired) {
		t.Errorf("Validate(stale auth_date) error = %v, want ErrExpired", err)
	}

	// Just under the TTL must still pass.
	if _, err := a.Validate(testInitData(t, time.Now().Add(-DefaultAuthTTL+time.Minute))); err != nil {
		t.Errorf("Validate(fresh auth_date) error = %v, want nil", err)
	}
}

func TestValidateHeader(t *testing.T) {
	a := NewAuthenticator(testBotToken, DefaultAuthTTL)
	valid := testInitData(t, time.Now())

	tests := []struct {
		name    string
		header  string
		wantErr error
	}{
		{"empty header", "", ErrNoAuth},
		{"wrong scheme", "Bearer " + valid, ErrNoAuth},
		{"scheme without payload", "tma", ErrNoAuth},
		{"valid tma", "tma " + valid, nil},
		{"case-insensitive scheme", "TMA " + valid, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := a.ValidateHeader(tt.header)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateHeader(%q) error = %v, want %v", tt.header, err, tt.wantErr)
			}
		})
	}
}
