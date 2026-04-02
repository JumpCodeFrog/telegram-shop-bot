package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type I18nService struct {
	locales map[string]map[string]string
	mu      sync.RWMutex
}

func NewI18nService(localesDir string) (*I18nService, error) {
	s := &I18nService{
		locales: make(map[string]map[string]string),
	}

	files, err := os.ReadDir(localesDir)
	if err != nil {
		return nil, fmt.Errorf("read locales dir: %w", err)
	}

	for _, f := range files {
		if filepath.Ext(f.Name()) != ".json" {
			continue
		}

		lang := filepath.Base(f.Name()[:len(f.Name())-len(".json")])
		data, err := os.ReadFile(filepath.Join(localesDir, f.Name()))
		if err != nil {
			return nil, fmt.Errorf("read locale file %s: %w", f.Name(), err)
		}

		var translations map[string]string
		if err := json.Unmarshal(data, &translations); err != nil {
			return nil, fmt.Errorf("parse locale file %s: %w", f.Name(), err)
		}

		s.locales[lang] = translations
	}

	return s, nil
}

func (s *I18nService) T(lang, key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if translations, ok := s.locales[lang]; ok {
		if text, ok := translations[key]; ok {
			return text
		}
	}

	// Fallback to English
	if translations, ok := s.locales["en"]; ok {
		if text, ok := translations[key]; ok {
			return text
		}
	}

	return key
}
