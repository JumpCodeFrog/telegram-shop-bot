package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

type ReferralService struct {
	bonusReferrerUSD float64
	bonusNewUserUSD  float64
	bonusReferrerPts int
	redis            *redis.Client
}

func NewReferralService(bonusRef, bonusNew float64, bonusPts int, rdb *redis.Client) *ReferralService {
	return &ReferralService{
		bonusReferrerUSD: bonusRef,
		bonusNewUserUSD:  bonusNew,
		bonusReferrerPts: bonusPts,
		redis:            rdb,
	}
}

const referralCodeTTL = 30 * 24 * time.Hour

// ReferralCode holds a generated code and its expiry time.
type ReferralCode struct {
	Code      string
	ExpiresAt time.Time
}

// GenerateCode returns a cryptographically random referral code. Bytes >= 248
// are rejected so that the modulo over the 62-character charset stays unbiased
// (248 = 4 * 62).
func (s *ReferralService) GenerateCode() ReferralCode {
	b := make([]byte, 8)
	for i := 0; i < len(b); {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand never fails on supported platforms; if it somehow
			// does, a panic beats silently issuing predictable codes.
			panic(fmt.Sprintf("referral: crypto/rand failed: %v", err))
		}
		for _, v := range buf {
			if v >= 248 {
				continue
			}
			b[i] = charset[int(v)%len(charset)]
			i++
			if i == len(b) {
				break
			}
		}
	}
	return ReferralCode{
		Code:      string(b),
		ExpiresAt: time.Now().Add(referralCodeTTL),
	}
}

// IsCodeExpired reports whether the given expiry time has passed.
// A zero time is treated as no expiry (always valid).
func IsCodeExpired(expiresAt time.Time) bool {
	return !expiresAt.IsZero() && time.Now().After(expiresAt)
}

func (s *ReferralService) GetReferralLink(botUsername, code string) string {
	return "https://t.me/" + botUsername + "?start=" + code
}

// CheckRegistrationLimit checks if the referrer has exceeded the hourly limit.
// Returns true if allowed, false if limit reached.
func (s *ReferralService) CheckRegistrationLimit(ctx context.Context, referrerID int64) (bool, error) {
	if s.redis == nil {
		return true, nil
	}
	key := fmt.Sprintf("antifraud:referral_rate:%d", referrerID)
	count, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		return false, err
	}

	if count == 1 {
		s.redis.Expire(ctx, key, time.Hour)
	}

	// Limit: max 5 referrals per hour
	if count > 5 {
		return false, nil
	}

	return true, nil
}

// ShouldAwardBonus returns true if the bonus should be awarded immediately.
// For anti-fraud, we might want to return false here and award it later on first purchase.
func (s *ReferralService) ShouldAwardBonus(isFirstPurchase bool) bool {
	return isFirstPurchase
}
