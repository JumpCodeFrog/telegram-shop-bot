package service

import (
	"context"
	"fmt"
	"math/rand"
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

func (s *ReferralService) GenerateCode() ReferralCode {
	b := make([]byte, 8)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
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
