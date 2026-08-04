package bot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestBotLocaleFilesCoverAllTranslationKeys(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob bot files: %v", err)
	}

	keyPattern := regexp.MustCompile(`b\.t\([^,]+,\s*"([^"]+)"\)`)
	keys := make(map[string]struct{})

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}

		for _, match := range keyPattern.FindAllStringSubmatch(string(data), -1) {
			keys[match[1]] = struct{}{}
		}
	}

	for _, localeName := range allLocales {
		localePath := filepath.Join("..", "..", "locales", localeName+".json")
		data, err := os.ReadFile(localePath)
		if err != nil {
			t.Fatalf("read %s: %v", localePath, err)
		}

		var translations map[string]string
		if err := json.Unmarshal(data, &translations); err != nil {
			t.Fatalf("parse %s: %v", localePath, err)
		}

		var missing []string
		for key := range keys {
			if _, ok := translations[key]; !ok {
				missing = append(missing, key)
			}
		}

		slices.Sort(missing)
		if len(missing) > 0 {
			t.Fatalf("%s locale is missing keys: %s", localeName, strings.Join(missing, ", "))
		}
	}
}

var allLocales = []string{"ru", "en", "es", "de", "zh"}

func loadLocale(t *testing.T, name string) map[string]string {
	t.Helper()

	localePath := filepath.Join("..", "..", "locales", name+".json")
	data, err := os.ReadFile(localePath)
	if err != nil {
		t.Fatalf("read %s: %v", localePath, err)
	}

	var translations map[string]string
	if err := json.Unmarshal(data, &translations); err != nil {
		t.Fatalf("parse %s: %v", localePath, err)
	}
	return translations
}

// TestLocaleFilesHaveIdenticalKeySets verifies key parity across all 5 locales:
// every locale must contain exactly the same set of keys as ru (the reference).
func TestLocaleFilesHaveIdenticalKeySets(t *testing.T) {
	t.Parallel()

	reference := loadLocale(t, "ru")

	for _, localeName := range allLocales[1:] {
		translations := loadLocale(t, localeName)

		var missing, extra []string
		for key := range reference {
			if _, ok := translations[key]; !ok {
				missing = append(missing, key)
			}
		}
		for key := range translations {
			if _, ok := reference[key]; !ok {
				extra = append(extra, key)
			}
		}

		slices.Sort(missing)
		slices.Sort(extra)
		if len(missing) > 0 {
			t.Errorf("%s locale is missing keys present in ru: %s", localeName, strings.Join(missing, ", "))
		}
		if len(extra) > 0 {
			t.Errorf("%s locale has keys absent from ru: %s", localeName, strings.Join(extra, ", "))
		}
	}
}
