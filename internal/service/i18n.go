package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// normalizeLang reduces an IETF/Telegram language tag to its lowercase primary
// subtag: "ru-RU" → "ru", "zh-hans-CN" → "zh". An empty tag falls back to "en".
func normalizeLang(lang string) string {
	if i := strings.IndexAny(lang, "-_"); i >= 0 {
		lang = lang[:i]
	}
	lang = strings.ToLower(lang)
	if lang == "" {
		return "en"
	}
	return lang
}

func (s *I18nService) T(lang, key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if translations, ok := s.locales[normalizeLang(lang)]; ok {
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

// Dict returns the full translation map for lang with English fallback:
// every English key is present, overridden by the requested locale where
// translated. The result is a copy the caller may own.
func (s *I18nService) Dict(lang string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]string, len(s.locales["en"]))
	for k, v := range s.locales["en"] {
		out[k] = v
	}
	if translations, ok := s.locales[normalizeLang(lang)]; ok {
		for k, v := range translations {
			out[k] = v
		}
	}
	return out
}

// Tf is a convenience wrapper that looks up the translation for key and formats
// it with fmt.Sprintf using the provided args. Useful for keys that contain %s/%d.
func (s *I18nService) Tf(lang, key string, args ...any) string {
	return fmt.Sprintf(s.T(lang, key), args...)
}
