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

	for _, localeName := range []string{"ru", "en"} {
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
