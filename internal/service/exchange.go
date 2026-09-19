package service

import (
	"math"
	"sync"
)

// ExchangeService holds the current USD→Stars conversion rate.
// The rate is set by Telegram's pricing (~50 Stars per $1) and rarely changes.
// Override at startup via the USD_TO_STARS_RATE environment variable.
type ExchangeService struct {
	mu         sync.RWMutex
	usdToStars int
	usdToRUB   float64
}

// NewExchangeService creates the service with the given initial rates.
// Pass config.USDToStarsRate (loaded from USD_TO_STARS_RATE env, default 50)
// and config.USDToRUBRate (loaded from USD_TO_RUB_RATE env, 0 = RUB disabled).
func NewExchangeService(usdToStarsRate int, usdToRUBRate float64) *ExchangeService {
	return &ExchangeService{usdToStars: usdToStarsRate, usdToRUB: usdToRUBRate}
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

// ConvertUSDToRUB converts a USD amount to RUB rounded to 2 decimal places.
// Returns 0 when the RUB rate is not configured (RUB payments disabled).
// The rate is scaled to kopecks first (rate*100, exact in float64 for
// realistic rates) so exact half-kopeck products such as 19.99 × 92.5 =
// 1849.075 round up to 1849.08 instead of dipping below the midpoint from
// intermediate rounding; math.Round(x)/100, never FormatFloat chains.
func (s *ExchangeService) ConvertUSDToRUB(amountUSD float64) float64 {
	s.mu.RLock()
	rate := s.usdToRUB
	s.mu.RUnlock()
	if rate <= 0 || amountUSD <= 0 {
		return 0
	}
	return math.Round(amountUSD*(rate*100)) / 100
}
