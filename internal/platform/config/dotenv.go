package config

import (
	"bufio"
	"os"
	"strings"
)

// loadDotEnvIfNeeded 在本地开发时自动 merge 工作区 .env（仅填充尚未设置的环境变量）。
// 避免 VS Code/Cursor Go 调试在 integratedTerminal 下把 envFile 展开成超长 /usr/bin/env 命令而被截断。
// 生产环境应通过真实环境变量注入；设 UNIO_SKIP_DOTENV=true 可显式关闭。
func loadDotEnvIfNeeded() {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("UNIO_SKIP_DOTENV")), "true") {
		return
	}

	for _, path := range []string{".env", "../.env", "../../.env"} {
		if mergeDotEnvFile(path) {
			return
		}
	}
}

// mergeDotEnvFile 读取 path 指向的 .env，仅为「当前未设置」的 key 写入 os.Setenv。
// 文件不存在返回 false；存在且读完返回 true。
func mergeDotEnvFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := parseEnvLine(line)
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}

	return scanner.Err() == nil
}

// parseEnvLine 解析 KEY=VALUE；不支持 export 前缀与多行值。
func parseEnvLine(line string) (key, value string, ok bool) {
	if strings.HasPrefix(line, "export ") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	}

	idx := strings.IndexByte(line, '=')
	if idx <= 0 {
		return "", "", false
	}

	key = strings.TrimSpace(line[:idx])
	if key == "" {
		return "", "", false
	}

	value = strings.TrimSpace(line[idx+1:])
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}

	return key, value, true
}
