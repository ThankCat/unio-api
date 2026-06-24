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
	// DATABASE_URL 可能已存在于运行环境（开发者 export 或本机 .env）；本用例要验证
	// 「未设置的 key 会被 .env 填充」，先用 t.Setenv 注册恢复，再 Unsetenv 清除，避免被环境值干扰。
	t.Setenv("DATABASE_URL", "")
	os.Unsetenv("DATABASE_URL")

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
	// 先注册恢复再清除，避免运行环境里已有的 DATABASE_URL 干扰，且测试后能还原。
	t.Setenv("DATABASE_URL", "")
	os.Unsetenv("DATABASE_URL")

	loadDotEnvIfNeeded()

	if got := os.Getenv("DATABASE_URL"); got != "" {
		t.Fatalf("expected skip, got DATABASE_URL=%q", got)
	}
}
