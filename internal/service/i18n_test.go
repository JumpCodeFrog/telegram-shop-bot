package service

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestI18n builds an I18nService from a temp locales dir with one known key
// per locale, so tests are independent of the real locale files.
func newTestI18n(t *testing.T) *I18nService {
	t.Helper()

	dir := t.TempDir()
	locales := map[string]string{
		"ru": `{"greeting": "Привет", "count": "Штук: %d"}`,
		"en": `{"greeting": "Hello", "count": "Count: %d", "en_only": "English only"}`,
		"es": `{"greeting": "Hola", "count": "Cantidad: %d"}`,
		"de": `{"greeting": "Hallo", "count": "Anzahl: %d"}`,
		"zh": `{"greeting": "你好", "count": "数量：%d"}`,
	}
	for lang, data := range locales {
		if err := os.WriteFile(filepath.Join(dir, lang+".json"), []byte(data), 0o644); err != nil {
			t.Fatalf("write %s locale: %v", lang, err)
		}
	}

	svc, err := NewI18nService(dir)
	if err != nil {
		t.Fatalf("NewI18nService: %v", err)
	}
	return svc
}

func TestT_NormalizesLanguageTags(t *testing.T) {
	t.Parallel()
	svc := newTestI18n(t)

	tests := []struct {
		lang string
		want string
	}{
		{"ru-RU", "Привет"},
		{"es-ES", "Hola"},
		{"de-AT", "Hallo"},
		{"zh-hans-CN", "你好"},
		{"", "Hello"},   // no locale → en
		{"xx", "Hello"}, // unknown locale → en fallback
		{"RU", "Привет"},
		{"ru_RU", "Привет"},
	}

	for _, tt := range tests {
		if got := svc.T(tt.lang, "greeting"); got != tt.want {
			t.Errorf("T(%q, greeting) = %q, want %q", tt.lang, got, tt.want)
		}
	}
}

func TestT_FallsBackToEnglishForMissingKey(t *testing.T) {
	t.Parallel()
	svc := newTestI18n(t)

	if got := svc.T("ru-RU", "en_only"); got != "English only" {
		t.Errorf("T(ru-RU, en_only) = %q, want fallback to English", got)
	}
	if got := svc.T("de-AT", "no_such_key"); got != "no_such_key" {
		t.Errorf("T(de-AT, no_such_key) = %q, want key itself", got)
	}
}

func TestTf_FormatsWithNormalizedTag(t *testing.T) {
	t.Parallel()
	svc := newTestI18n(t)

	if got := svc.Tf("zh-hans-CN", "count", 7); got != "数量：7" {
		t.Errorf("Tf(zh-hans-CN, count, 7) = %q", got)
	}
	if got := svc.Tf("", "count", 3); got != "Count: 3" {
		t.Errorf("Tf(\"\", count, 3) = %q", got)
	}
}
