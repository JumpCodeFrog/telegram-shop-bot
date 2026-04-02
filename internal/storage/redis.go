package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type FSMStore interface {
	SetAddProductState(ctx context.Context, userID int64, state *AddProductState, ttl time.Duration) error
	GetAddProductState(ctx context.Context, userID int64) (*AddProductState, error)
	DelAddProductState(ctx context.Context, userID int64) error

	SetPromoState(ctx context.Context, userID int64, enteredAt time.Time, ttl time.Duration) error
	GetPromoState(ctx context.Context, userID int64) (time.Time, error)
	DelPromoState(ctx context.Context, userID int64) error
}

type RedisFSMStore struct {
	client *redis.Client
}

func NewRedisFSMStore(addr, password string) *RedisFSMStore {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})
	return &RedisFSMStore{client: rdb}
}

func (s *RedisFSMStore) Client() *redis.Client {
	return s.client
}

func (s *RedisFSMStore) SetAddProductState(ctx context.Context, userID int64, state *AddProductState, ttl time.Duration) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("fsm:add_product:%d", userID)
	return s.client.Set(ctx, key, data, ttl).Err()
}

func (s *RedisFSMStore) GetAddProductState(ctx context.Context, userID int64) (*AddProductState, error) {
	key := fmt.Sprintf("fsm:add_product:%d", userID)
	val, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state AddProductState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *RedisFSMStore) DelAddProductState(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("fsm:add_product:%d", userID)
	return s.client.Del(ctx, key).Err()
}

func (s *RedisFSMStore) SetPromoState(ctx context.Context, userID int64, enteredAt time.Time, ttl time.Duration) error {
	key := fmt.Sprintf("fsm:promo:%d", userID)
	return s.client.Set(ctx, key, enteredAt.Unix(), ttl).Err()
}

func (s *RedisFSMStore) GetPromoState(ctx context.Context, userID int64) (time.Time, error) {
	key := fmt.Sprintf("fsm:promo:%d", userID)
	val, err := s.client.Get(ctx, key).Int64()
	if err == redis.Nil {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(val, 0), nil
}

func (s *RedisFSMStore) DelPromoState(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("fsm:promo:%d", userID)
	return s.client.Del(ctx, key).Err()
}
