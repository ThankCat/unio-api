package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeDotEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(`
# comment
GATEWAY_HTTP_ADDR=:9999
DATABASE_URL=postgres://local/unio
`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GATEWAY_HTTP_ADDR", ":8520")
	t.Setenv("UNIO_SKIP_DOTENV", "true")

	if !mergeDotEnvFile(path) {
		t.Fatal("expected merge to succeed")
	}
	if got := os.Getenv("GATEWAY_HTTP_ADDR"); got != ":8520" {
		t.Fatalf("existing env should not be overwritten, got %q", got)
	}
	if got := os.Getenv("DATABASE_URL"); got != "postgres://local/unio" {
		t.Fatalf("unset env should be filled, got %q", got)
	}
}

func TestLoadDotEnvIfNeededSkipsWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("DATABASE_URL=postgres://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(dir)
	t.Setenv("UNIO_SKIP_DOTENV", "true")
	os.Unsetenv("DATABASE_URL")

	loadDotEnvIfNeeded()

	if got := os.Getenv("DATABASE_URL"); got != "" {
		t.Fatalf("expected skip, got DATABASE_URL=%q", got)
	}
}
