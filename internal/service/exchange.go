package service

import (
	"sync"
)

// ExchangeService holds the current USD→Stars conversion rate.
// The rate is set by Telegram's pricing (~50 Stars per $1) and rarely changes.
// Override at startup via the USD_TO_STARS_RATE environment variable.
type ExchangeService struct {
	mu         sync.RWMutex
	usdToStars int
}

// NewExchangeService creates the service with the given initial rate.
// Pass config.USDToStarsRate (loaded from USD_TO_STARS_RATE env, default 50).
func NewExchangeService(usdToStarsRate int) *ExchangeService {
	return &ExchangeService{usdToStars: usdToStarsRate}
}

// GetUSDToStarsRate returns the current exchange rate.
func (s *ExchangeService) GetUSDToStarsRate() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usdToStars
}

// SetRate updates the exchange rate. Safe for concurrent use.
func (s *ExchangeService) SetRate(rate int) {
	s.mu.Lock()
	s.usdToStars = rate
	s.mu.Unlock()
}

// ConvertUSDToStars converts a USD amount to Telegram Stars.
// Returns at least 1 for any positive amount.
func (s *ExchangeService) ConvertUSDToStars(amountUSD float64) int {
	s.mu.RLock()
	rate := s.usdToStars
	s.mu.RUnlock()

	stars := int(amountUSD * float64(rate))
	if stars < 1 && amountUSD > 0 {
		return 1
	}
	return stars
}
