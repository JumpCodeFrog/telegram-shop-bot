package service

import (
	"context"
)

type LoyaltyStore interface {
	AddPoints(ctx context.Context, userID int64, pts int, reason string, refID string) error
	GetPoints(ctx context.Context, userID int64) (int, string, error)
	UpdateLevel(ctx context.Context, userID int64, level string) error
	AddAchievement(ctx context.Context, userID int64, achievementID int64) error
	HasAchievement(ctx context.Context, userID int64, code string) (bool, error)
}

type LoyaltyService struct {
	store        LoyaltyStore
	premiumBonus int
}

func NewLoyaltyService(store LoyaltyStore, premiumBonus int) *LoyaltyService {
	return &LoyaltyService{
		store:        store,
		premiumBonus: premiumBonus,
	}
}

// CalculateCashback computes points based on level and premium status.
func (s *LoyaltyService) CalculateCashback(amountUSD float64, level string, isPremium bool) int {
	basePct := 1.0 // Bronze
	switch level {
	case "silver":
		basePct = 2.0
	case "gold":
		basePct = 5.0
	case "vip":
		basePct = 10.0
	}
	if isPremium {
		basePct += float64(s.premiumBonus)
	}
	return int(amountUSD * (basePct / 100.0) * 100) // 1 USD = 100 base pts, then pct
}

// CheckAndUpgradeLevel checks if the user has earned a new level.
// Returns the new level name and true if an upgrade occurred.
// The caller is responsible for sending any user-facing notifications.
func (s *LoyaltyService) CheckAndUpgradeLevel(ctx context.Context, userID int64, currentLevel string, totalPts int) (string, bool) {
	newLevel := currentLevel
	if totalPts >= 10000 && currentLevel != "vip" {
		newLevel = "vip"
	} else if totalPts >= 5000 && currentLevel != "gold" && currentLevel != "vip" {
		newLevel = "gold"
	} else if totalPts >= 1000 && currentLevel == "bronze" {
		newLevel = "silver"
	}

	if newLevel == currentLevel {
		return currentLevel, false
	}

	_ = s.store.UpdateLevel(ctx, userID, newLevel)
	return newLevel, true
}
