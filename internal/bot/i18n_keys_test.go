package bot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

// printfVerbPattern matches one Go printf verb: an optional explicit index
// %[N], flags, width, an optional precision and a single verb letter. A match
// whose verb letter is immediately followed by another ASCII letter is a
// literal percent sign in prose ("10% discount", "10%-Rabatt-Promocode") and
// is rejected by extractPrintfVerbs: Go's fmt would parse such a spot as a
// verb, but the keys carrying them are rendered through b.t without Sprintf,
// and every genuine format verb in the locale corpus is followed by a space,
// punctuation, a newline or the end of the string. Keep it that way when
// adding keys (write "%d XTR", never "%dXTR").
var printfVerbPattern = regexp.MustCompile(`%(?:\[(\d+)\])?[-+ #0]*(?:\*|\d+)?(?:\.(\*|\d+))?([a-zA-Z])`)

// printfVerb is one normalized verb occurrence inside a translation.
type printfVerb struct {
	index    int    // explicit %[N] index; meaningful only when explicit is true
	explicit bool   // the verb carried an explicit %[N] index
	spec     string // the verb type: optional "."+precision plus the verb letter ("d", "s", ".2f")
}

// extractPrintfVerbs returns the printf verbs of one translation in order of
// appearance. Bare %% escapes are masked out first (they are literal percent
// signs, not verbs); every real verb in the locale files is %% -free around
// its own match, so masking with two placeholder bytes keeps offsets intact
// for the prose-percent check.
func extractPrintfVerbs(value string) []printfVerb {
	masked := strings.ReplaceAll(value, "%%", "\x00\x00")
	var verbs []printfVerb
	for _, m := range printfVerbPattern.FindAllStringSubmatchIndex(masked, -1) {
		if m[1] < len(masked) && isASCIILetter(masked[m[1]]) {
			continue // literal percent in prose: "10% discount", "10%-Rabatt"
		}
		v := printfVerb{spec: masked[m[6]:m[7]]}
		if m[4] >= 0 {
			v.spec = "." + masked[m[4]:m[5]] + v.spec
		}
		if m[2] >= 0 {
			idx, err := strconv.Atoi(masked[m[2]:m[3]])
			if err != nil || idx < 1 {
				idx = -1 // absurd index: canonicalVerbSpecs rejects it loudly
			}
			v.explicit = true
			v.index = idx
		}
		verbs = append(verbs, v)
	}
	return verbs
}

func isASCIILetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// canonicalVerbSpecs normalizes a verb sequence to the positional argument
// type list fmt.Sprintf will consume, bounded by maxPos reference positions.
// Positional verbs contribute their specs in order of appearance. Explicit
// index verbs must ALL be indexed (a mixed sequence resumes positional
// consumption after the last index — uncheckable cleverness) and must cover
// exactly the positions 1..maxPos with a consistent type per position; an
// uncovered position stays "" so the equality check against the reference
// fails. Sequences that cannot be normalized return an error naming why.
func canonicalVerbSpecs(verbs []printfVerb, maxPos int) ([]string, error) {
	if len(verbs) == 0 {
		return nil, nil
	}
	explicit := 0
	for _, v := range verbs {
		if v.explicit {
			explicit++
		}
	}
	switch {
	case explicit == 0:
		specs := make([]string, len(verbs))
		for i, v := range verbs {
			specs[i] = v.spec
		}
		return specs, nil
	case explicit < len(verbs):
		return nil, fmt.Errorf("mixes positional verbs with explicit-index verbs")
	}
	specs := make([]string, maxPos)
	for _, v := range verbs {
		if v.index < 1 || v.index > maxPos {
			return nil, fmt.Errorf("explicit index %d outside the reference argument range 1..%d", v.index, maxPos)
		}
		if prev := specs[v.index-1]; prev != "" && prev != v.spec {
			return nil, fmt.Errorf("explicit index %d reused with types %q and %q", v.index, prev, v.spec)
		}
		specs[v.index-1] = v.spec
	}
	return specs, nil
}

// TestLocaleFilesHaveMatchingPrintfVerbs verifies printf verb parity across
// all 5 locales with en as the reference (the bot's default language — call
// sites order their Sprintf arguments to match it; the key-set test above
// uses ru for coverage, which is a different property). Every key's value in
// every other locale must either (a) repeat en's verb-type sequence exactly
// with positional verbs, or (b) use explicit indices (%[1]d, %[2]s, …) that
// cover exactly en's argument positions with matching types — form (b) is
// how a translation whose natural word order differs (e.g. zh naming the
// order before the refund id) stays idiomatic instead of being butchered to
// satisfy verb order. A sequence that satisfies neither renders %!d(string=…)
// garble for that locale's admins — the exact bug this test pins. Keys
// without verbs pass trivially; bare %% is ignored.
func TestLocaleFilesHaveMatchingPrintfVerbs(t *testing.T) {
	t.Parallel()

	locales := make(map[string]map[string]string, len(allLocales))
	for _, name := range allLocales {
		locales[name] = loadLocale(t, name)
	}
	reference := locales["en"]

	keys := make([]string, 0, len(reference))
	for key := range reference {
		keys = append(keys, key)
	}
	slices.Sort(keys) // deterministic failure order

	for _, key := range keys {
		refVerbs := extractPrintfVerbs(reference[key])
		refSpecs, err := canonicalVerbSpecs(refVerbs, len(refVerbs))
		if err != nil {
			t.Errorf("en: key %q: unparsable reference verbs: %v", key, err)
			continue
		}
		for _, localeName := range allLocales {
			if localeName == "en" {
				continue
			}
			value, ok := locales[localeName][key]
			if !ok {
				continue // absent keys are the key-set test's job
			}
			gotVerbs := extractPrintfVerbs(value)
			gotSpecs, err := canonicalVerbSpecs(gotVerbs, len(refSpecs))
			if err != nil {
				t.Errorf("%s: key %q: %v (en reference verbs: [%s])",
					localeName, key, err, strings.Join(refSpecs, ","))
				continue
			}
			if !slices.Equal(gotSpecs, refSpecs) {
				t.Errorf("%s: key %q: printf verb mismatch: en wants [%s], %s has [%s]",
					localeName, key, strings.Join(refSpecs, ","), localeName, strings.Join(gotSpecs, ","))
			}
		}
	}
}
