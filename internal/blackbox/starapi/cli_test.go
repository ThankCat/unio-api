//go:build blackbox

package starapi_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

const realCLITimeout = 3 * time.Minute

func TestStarAPICodexCLI(t *testing.T) {
	requireCLIRealUpstream(t)
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex CLI is not installed")
	}

	f := setupStarAPIOpenAI(t)
	ctx, cancel := context.WithTimeout(context.Background(), realCLITimeout)
	defer cancel()
	workDir := t.TempDir()
	lastMessagePath := filepath.Join(workDir, "last-message.txt")

	cmd := exec.CommandContext(ctx, codexPath,
		"exec",
		"--ignore-user-config",
		"--ephemeral",
		"--skip-git-repo-check",
		"--sandbox", "read-only",
		"--color", "never",
		"--output-last-message", lastMessagePath,
		"--model", f.ModelID,
		"--config", `model_provider="unio"`,
		"--config", `model_providers.unio.name="Unio"`,
		"--config", `model_providers.unio.base_url="`+f.BaseURL+`"`,
		"--config", `model_providers.unio.env_key="UNIO_API_KEY"`,
		"--config", `model_providers.unio.wire_api="responses"`,
		"--config", `model_reasoning_effort="low"`,
		"Reply with exactly: ok. Do not call tools.",
	)
	cmd.Dir = workDir
	cmd.Env = minimalCLIEnv(map[string]string{
		"CODEX_HOME":   t.TempDir(),
		"UNIO_API_KEY": f.APIKey,
	})
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Codex CLI failed: %v\n%s", err, sanitizedCLIOutput(output, f.APIKey))
	}
	lastMessage, err := os.ReadFile(lastMessagePath)
	if err != nil {
		t.Fatalf("read Codex CLI final message: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(lastMessage)), "ok") {
		t.Fatalf("Codex CLI returned no expected answer: %s", sanitizedCLIOutput(lastMessage, f.APIKey))
	}

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "openai",
		Endpoint:       "responses",
		Stream:          true,
	})
}

func TestStarAPIClaudeCLI(t *testing.T) {
	requireCLIRealUpstream(t)
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("Claude CLI is not installed")
	}

	f := setupStarAPIAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), realCLITimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudePath,
		"--bare",
		"--print",
		"--no-session-persistence",
		"--tools", "",
		"--model", f.ModelID,
		"--effort", "low",
		"--max-budget-usd", "0.25",
		"--system-prompt", "Return only the requested answer and do not use tools.",
		"--output-format", "text",
		"Reply with exactly: ok. Do not call tools.",
	)
	cmd.Dir = t.TempDir()
	cmd.Env = isolatedClaudeCLIEnv(t, f.APIKey, f.AnthropicBaseURL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Claude CLI failed: %v\n%s", err, sanitizedCLIOutput(output, f.APIKey))
	}
	if !strings.Contains(strings.ToLower(string(output)), "ok") {
		t.Fatalf("Claude CLI returned no expected answer: %s", sanitizedCLIOutput(output, f.APIKey))
	}

	f.AssertLatestRequestFacts(t, sdkfixture.RequestFactsExpectation{
		IngressProtocol: "anthropic",
		Endpoint:       "messages",
		Stream:          true,
	})
}

func TestIsolatedClaudeCLIEnvIgnoresPersonalConfiguration(t *testing.T) {
	personalConfigDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", personalConfigDir)
	t.Setenv("ANTHROPIC_BASE_URL", "https://personal-config.invalid")

	env := isolatedClaudeCLIEnv(t, "sk-test", "http://127.0.0.1:8520/")
	configDir, configDirCount := cliEnvValue(env, "CLAUDE_CONFIG_DIR")
	if configDirCount != 1 {
		t.Fatalf("CLAUDE_CONFIG_DIR entries = %d, want 1", configDirCount)
	}
	if configDir == personalConfigDir {
		t.Fatal("Claude CLI environment reused the personal configuration directory")
	}
	if got, count := cliEnvValue(env, "ANTHROPIC_BASE_URL"); count != 1 || got != "http://127.0.0.1:8520/" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q (entries=%d), want isolated fixture URL", got, count)
	}
	if got, count := cliEnvValue(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"); count != 1 || got != "1" {
		t.Fatalf("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = %q (entries=%d), want 1", got, count)
	}
}

func requireCLIRealUpstream(t *testing.T) {
	t.Helper()
	if os.Getenv("STARAPI_CLI_BLACKBOX") != "1" {
		t.Skip("STARAPI_CLI_BLACKBOX is not set to 1")
	}
}

func minimalCLIEnv(overrides map[string]string) []string {
	env := make([]string, 0, 10+len(overrides))
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "LANG", "LC_ALL", "TZ"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "NO_PROXY=127.0.0.1,localhost")
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func isolatedClaudeCLIEnv(t *testing.T, apiKey, baseURL string) []string {
	t.Helper()
	// --bare still loads the user settings env block, which can override the fixture URL.
	return minimalCLIEnv(map[string]string{
		"ANTHROPIC_API_KEY":                        apiKey,
		"ANTHROPIC_BASE_URL":                       baseURL,
		"CLAUDE_CONFIG_DIR":                        t.TempDir(),
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	})
}

func cliEnvValue(env []string, key string) (string, int) {
	prefix := key + "="
	var value string
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
			count++
		}
	}
	return value, count
}

func sanitizedCLIOutput(output []byte, secrets ...string) string {
	const maxOutput = 2_000
	trimmed := strings.TrimSpace(string(output))
	for _, secret := range secrets {
		if secret != "" {
			trimmed = strings.ReplaceAll(trimmed, secret, "[REDACTED]")
		}
	}
	if len(trimmed) > maxOutput {
		return trimmed[:maxOutput] + "..."
	}
	return trimmed
}
