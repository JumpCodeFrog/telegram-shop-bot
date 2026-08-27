package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testToken = "123456789:abcdefghijklmnopqrstuvwxyz_ABCD"

type fakeVerifier struct {
	identity BotIdentity
	err      error
	calls    int
}

func (f *fakeVerifier) Verify(context.Context, string) (BotIdentity, error) {
	f.calls++
	return f.identity, f.err
}

func TestRunInitCreatesPrivateConfiguration(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	verifier := &fakeVerifier{identity: BotIdentity{ID: 9, Username: "verified_shop_bot"}}
	var output bytes.Buffer

	result, err := RunInit(context.Background(), InitOptions{
		EnvPath:  envPath,
		In:       strings.NewReader(testToken + "\n42\n"),
		Out:      &output,
		Verifier: verifier,
	})
	if err != nil {
		t.Fatalf("RunInit() error = %v", err)
	}
	if !result.Created {
		t.Fatal("Created = false")
	}
	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte("BOT_USERNAME=verified_shop_bot")) || !bytes.Contains(content, []byte("ADMIN_IDS=42")) {
		t.Fatalf("unexpected config:\n%s", content)
	}
	if strings.Contains(output.String(), testToken) {
		t.Fatal("stdout leaked token")
	}
	if info, err := os.Stat(envPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v; want 0600", info.Mode().Perm(), err)
	}
	for _, name := range []string{"data", "backups"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %v, err = %v; want directory 0700", name, info.Mode().Perm(), err)
		}
	}
}

func TestRunInitInputFailuresLeaveNoConfig(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		verifyErr error
	}{
		{name: "empty token", input: "\n42\n"},
		{name: "placeholder token", input: "123456789:AAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n42\n"},
		{name: "path-like token", input: "123:secret/path\n42\n"},
		{name: "token EOF", input: ""},
		{name: "Telegram rejected", input: testToken + "\n42\n", verifyErr: ErrTelegramCheck},
		{name: "admin non numeric", input: testToken + "\nhello\n"},
		{name: "admin negative", input: testToken + "\n-42\n"},
		{name: "admin zero", input: testToken + "\n0\n"},
		{name: "admin EOF", input: testToken + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			envPath := filepath.Join(dir, ".env")
			verifier := &fakeVerifier{
				identity: BotIdentity{ID: 9, Username: "verified_shop_bot"},
				err:      tt.verifyErr,
			}
			var output bytes.Buffer
			_, err := RunInit(context.Background(), InitOptions{EnvPath: envPath, In: strings.NewReader(tt.input), Out: &output, Verifier: verifier})
			if err == nil {
				t.Fatal("RunInit() error = nil")
			}
			if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
				t.Fatalf("configuration was created after failure: %v", statErr)
			}
			if strings.Contains(output.String(), testToken) || strings.Contains(err.Error(), testToken) {
				t.Fatal("token leaked in error or output")
			}
		})
	}
}

func TestRunInitExistingFileIsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := []byte("EXISTING=must-stay-byte-identical\n")
	if err := os.WriteFile(envPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	verifier := &fakeVerifier{err: errors.New("must not be called")}

	result, err := RunInit(context.Background(), InitOptions{EnvPath: envPath, In: strings.NewReader(""), Out: &bytes.Buffer{}, Verifier: verifier})
	if err != nil {
		t.Fatalf("RunInit() error = %v", err)
	}
	if result.Created || verifier.calls != 0 {
		t.Fatalf("result = %+v, verifier calls = %d", result, verifier.calls)
	}
	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("existing file changed: got %q, want %q", got, original)
	}
}

func TestRunInitRejectsNonRegularConfigurationPath(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		envPath := filepath.Join(t.TempDir(), ".env")
		if err := os.Mkdir(envPath, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := RunInit(context.Background(), InitOptions{EnvPath: envPath, In: strings.NewReader(""), Out: &bytes.Buffer{}, Verifier: &fakeVerifier{}})
		if err == nil {
			t.Fatal("RunInit() error = nil for directory path")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating a symlink can require elevated Windows privileges")
		}
		dir := t.TempDir()
		target := filepath.Join(dir, "target.env")
		original := []byte("TARGET=must-stay-unchanged\n")
		if err := os.WriteFile(target, original, 0o600); err != nil {
			t.Fatal(err)
		}
		envPath := filepath.Join(dir, ".env")
		if err := os.Symlink(target, envPath); err != nil {
			t.Fatal(err)
		}
		_, err := RunInit(context.Background(), InitOptions{EnvPath: envPath, In: strings.NewReader(""), Out: &bytes.Buffer{}, Verifier: &fakeVerifier{}})
		if err == nil {
			t.Fatal("RunInit() error = nil for symlink path")
		}
		got, readErr := os.ReadFile(target)
		if readErr != nil || !bytes.Equal(got, original) {
			t.Fatalf("symlink target changed: got %q, err = %v", got, readErr)
		}
	})
}

func TestRunInitBattleTwentyFreshDirectories(t *testing.T) {
	for i := 1; i <= 20; i++ {
		t.Run(fmt.Sprintf("run-%02d", i), func(t *testing.T) {
			dir := t.TempDir()
			envPath := filepath.Join(dir, ".env")
			_, err := RunInit(context.Background(), InitOptions{
				EnvPath:  envPath,
				In:       strings.NewReader(fmt.Sprintf("  %s  \n%d\n", testToken, i)),
				Out:      &bytes.Buffer{},
				Verifier: &fakeVerifier{identity: BotIdentity{ID: 9, Username: "verified_shop_bot"}},
			})
			if err != nil {
				t.Fatalf("RunInit() error = %v", err)
			}
			info, err := os.Stat(envPath)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %v, err = %v", info.Mode().Perm(), err)
			}
		})
	}
}
