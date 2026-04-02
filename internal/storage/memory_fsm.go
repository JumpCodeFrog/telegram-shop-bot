package storage

import (
	"context"
	"sync"
	"time"
)

type memoryAddProductEntry struct {
	state     *AddProductState
	expiresAt time.Time
}

type memoryPromoEntry struct {
	enteredAt time.Time
	expiresAt time.Time
}

type MemoryFSMStore struct {
	mu          sync.RWMutex
	addProducts map[int64]memoryAddProductEntry
	promos      map[int64]memoryPromoEntry
}

func NewMemoryFSMStore() *MemoryFSMStore {
	return &MemoryFSMStore{
		addProducts: make(map[int64]memoryAddProductEntry),
		promos:      make(map[int64]memoryPromoEntry),
	}
}

func (s *MemoryFSMStore) SetAddProductState(_ context.Context, userID int64, state *AddProductState, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addProducts[userID] = memoryAddProductEntry{
		state:     state,
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}

func (s *MemoryFSMStore) GetAddProductState(_ context.Context, userID int64) (*AddProductState, error) {
	s.mu.RLock()
	entry, ok := s.addProducts[userID]
	s.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	if time.Now().After(entry.expiresAt) {
		s.mu.Lock()
		delete(s.addProducts, userID)
		s.mu.Unlock()
		return nil, nil
	}
	return entry.state, nil
}

func (s *MemoryFSMStore) DelAddProductState(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.addProducts, userID)
	return nil
}

func (s *MemoryFSMStore) SetPromoState(_ context.Context, userID int64, enteredAt time.Time, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.promos[userID] = memoryPromoEntry{
		enteredAt: enteredAt,
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}

func (s *MemoryFSMStore) GetPromoState(_ context.Context, userID int64) (time.Time, error) {
	s.mu.RLock()
	entry, ok := s.promos[userID]
	s.mu.RUnlock()
	if !ok {
		return time.Time{}, nil
	}
	if time.Now().After(entry.expiresAt) {
		s.mu.Lock()
		delete(s.promos, userID)
		s.mu.Unlock()
		return time.Time{}, nil
	}
	return entry.enteredAt, nil
}

func (s *MemoryFSMStore) DelPromoState(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.promos, userID)
	return nil
}
