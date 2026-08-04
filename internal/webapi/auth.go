// Package webapi serves the Telegram Mini App: initData authentication and
// the JSON REST API consumed by web/app.
package webapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultAuthTTL bounds how old an initData auth_date may be before the
// request is rejected as a replay.
const DefaultAuthTTL = time.Hour

var (
	// ErrNoAuth reports a missing or malformed `Authorization: tma <initData>` header.
	ErrNoAuth = errors.New("webapi: missing or malformed Authorization header")
	// ErrBadSignature reports an initData whose HMAC does not verify.
	ErrBadSignature = errors.New("webapi: initData signature mismatch")
	// ErrExpired reports an initData whose auth_date is older than the TTL.
	ErrExpired = errors.New("webapi: initData auth_date expired")
)

// WebAppUser is the `user` JSON object embedded in Mini App initData.
type WebAppUser struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
	IsPremium    bool   `json:"is_premium"`
}

// AuthResult is a successfully validated initData.
type AuthResult struct {
	User     WebAppUser
	AuthDate time.Time
}

// Authenticator validates Mini App initData per the Telegram spec:
// secret = HMAC_SHA256(key="WebAppData", msg=botToken), then the hex `hash`
// field must equal HMAC_SHA256(key=secret, msg=data-check-string) where the
// data-check-string is all key=value pairs except hash, sorted alphabetically
// and joined with "\n".
type Authenticator struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time // test seam
}

// NewAuthenticator derives the initData secret from the bot token.
// A non-positive ttl falls back to DefaultAuthTTL.
func NewAuthenticator(botToken string, ttl time.Duration) *Authenticator {
	if ttl <= 0 {
		ttl = DefaultAuthTTL
	}
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(botToken))
	return &Authenticator{secret: mac.Sum(nil), ttl: ttl, now: time.Now}
}

// ValidateHeader validates an `Authorization: tma <initData>` header value.
func (a *Authenticator) ValidateHeader(header string) (*AuthResult, error) {
	scheme, initData, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "tma") {
		return nil, ErrNoAuth
	}
	return a.Validate(strings.TrimSpace(initData))
}

// Validate checks the signature and freshness of a raw initData query string
// and extracts the authenticated user.
func (a *Authenticator) Validate(initData string) (*AuthResult, error) {
	values, err := url.ParseQuery(initData)
	if err != nil {
		return nil, fmt.Errorf("webapi: parse initData: %w", err)
	}

	gotHex := values.Get("hash")
	if gotHex == "" {
		return nil, ErrBadSignature
	}
	values.Del("hash")

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var dcs strings.Builder
	for i, k := range keys {
		if i > 0 {
			dcs.WriteByte('\n')
		}
		dcs.WriteString(k)
		dcs.WriteByte('=')
		dcs.WriteString(values.Get(k))
	}

	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(dcs.String()))
	want := mac.Sum(nil)

	got, err := hex.DecodeString(gotHex)
	if err != nil || !hmac.Equal(got, want) {
		return nil, ErrBadSignature
	}

	// Signature is genuine from here on; auth_date is trusted data.
	unix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("webapi: parse auth_date: %w", err)
	}
	authDate := time.Unix(unix, 0)
	if a.now().Sub(authDate) > a.ttl {
		return nil, ErrExpired
	}

	var user WebAppUser
	if raw := values.Get("user"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &user); err != nil {
			return nil, fmt.Errorf("webapi: parse initData user: %w", err)
		}
	}
	if user.ID == 0 {
		return nil, errors.New("webapi: initData carries no user")
	}

	return &AuthResult{User: user, AuthDate: authDate}, nil
}

// SignInitData produces a valid initData query string for the given bot token
// and pre-filled values (hash is overwritten). Exported for tests and smoke
// tooling; production code never signs, only validates.
func SignInitData(botToken string, values url.Values) string {
	values.Del("hash")

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var dcs strings.Builder
	for i, k := range keys {
		if i > 0 {
			dcs.WriteByte('\n')
		}
		dcs.WriteString(k)
		dcs.WriteByte('=')
		dcs.WriteString(values.Get(k))
	}

	secretMac := hmac.New(sha256.New, []byte("WebAppData"))
	secretMac.Write([]byte(botToken))
	mac := hmac.New(sha256.New, secretMac.Sum(nil))
	mac.Write([]byte(dcs.String()))
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))

	return values.Encode()
}
