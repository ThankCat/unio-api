package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
)

func TestGatewayJSONEnvelope(t *testing.T) {
	runtime, path := newTestGatewayRuntime(t, zapcore.DebugLevel, 100)
	Debug(runtime.Logger, "routing", "sticky", "sticky hit",
		zap.String("trace_id", "stale-trace"),
		zap.Int64("channel_id", 42),
		zap.String("trace_id", "trace-1"),
	)
	if err := runtime.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	records := readGatewayRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	record := records[0]
	if record["server"] != "gateway" || record["environment"] != "test" || record["instance"] != "gw-test" ||
		record["type"] != "routing" || record["event"] != "sticky" {
		t.Fatalf("envelope = %#v", record)
	}
	if record["level"] != "debug" || record["message"] != "sticky hit" {
		t.Fatalf("entry = %#v", record)
	}
	data := record["data"].(map[string]any)
	if data["trace_id"] != "trace-1" || data["channel_id"] != float64(42) {
		t.Fatalf("data = %#v", data)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(raw, []byte(`"trace_id":`)); got != 1 {
		t.Fatalf("raw JSON contains %d trace_id keys: %s", got, raw)
	}
}

func TestGatewayWarningUsesCanonicalLevelName(t *testing.T) {
	runtime, path := newTestGatewayRuntime(t, zapcore.InfoLevel, 100)
	Warn(runtime.Logger, "upstream", "attempt", "upstream attempt failed")
	_ = runtime.Close()
	if got := readGatewayRecords(t, path)[0]["level"]; got != "warning" {
		t.Fatalf("level = %v", got)
	}
}

func TestGatewayTemporaryDebugExpiresLocally(t *testing.T) {
	runtime, path := newTestGatewayRuntime(t, zapcore.InfoLevel, 100)
	Debug(runtime.Logger, "http", "request", "before debug")
	now := time.Now()
	err := runtime.ApplyDebugSession(DebugSession{
		SessionID: "dbg-1",
		ExpiresAt: now.Add(80 * time.Millisecond),
		Reason:    "test slow request",
		Revision:  7,
	}, now)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	Debug(runtime.Logger, "http", "request", "during debug")
	time.Sleep(130 * time.Millisecond)
	Debug(runtime.Logger, "http", "request", "after debug")
	if status := runtime.Status(); status.EffectiveLevel != "info" || status.DebugSessionID != "" || status.AppliedRevision != 7 {
		t.Fatalf("status = %+v", status)
	}
	_ = runtime.Close()

	records := readGatewayRecords(t, path)
	messages := make([]string, 0, len(records))
	for _, record := range records {
		messages = append(messages, record["message"].(string))
	}
	joined := strings.Join(messages, ",")
	if strings.Contains(joined, "before debug") || strings.Contains(joined, "after debug") {
		t.Fatalf("disabled debug leaked: %s", joined)
	}
	if !strings.Contains(joined, "during debug") || !strings.Contains(joined, "debug logging expired") {
		t.Fatalf("missing dynamic logs: %s", joined)
	}
}

func TestGatewayDebugSessionValidation(t *testing.T) {
	runtime, _ := newTestGatewayRuntime(t, zapcore.InfoLevel, 100)
	defer runtime.Close()
	now := time.Now()
	for _, session := range []DebugSession{
		{ExpiresAt: now.Add(time.Minute), Reason: "reason"},
		{SessionID: "dbg", ExpiresAt: now.Add(time.Minute)},
		{SessionID: "dbg", ExpiresAt: now.Add(61 * time.Minute), Reason: "reason"},
		{SessionID: "dbg", ExpiresAt: now.Add(time.Minute), Reason: "bad\nreason"},
	} {
		if err := runtime.ApplyDebugSession(session, now); err == nil {
			t.Fatalf("expected validation error for %+v", session)
		}
	}
	if err := validateDebugSession(DebugSession{
		SessionID: "dbg-unicode", ExpiresAt: now.Add(time.Minute), Reason: strings.Repeat("慢", 200), Revision: 1,
	}, now); err != nil {
		t.Fatalf("200 Unicode characters must be accepted: %v", err)
	}
}

func TestGatewayOversizedRecordIsTruncated(t *testing.T) {
	runtime, path := newTestGatewayRuntime(t, zapcore.InfoLevel, 100)
	Info(runtime.Logger, "system", "settings", "setting updated",
		zap.String("trace_id", "trace-large"),
		zap.String("oversized", strings.Repeat("x", maxGatewayLogLineSize+1024)),
	)
	_ = runtime.Close()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > maxGatewayLogLineSize {
		t.Fatalf("line size = %d", len(content))
	}
	record := readGatewayRecords(t, path)[0]
	data := record["data"].(map[string]any)
	if data["record_truncated"] != true || data["trace_id"] != "trace-large" {
		t.Fatalf("truncated data = %#v", data)
	}
}

func TestGatewayFileRotationDelaysThenCompressesHistory(t *testing.T) {
	runtime, path := newTestGatewayRuntime(t, zapcore.InfoLevel, 1)
	payload := strings.Repeat("x", 600<<10)
	for i := 0; i < 4; i++ {
		Info(runtime.Logger, "system", "service", "rotation probe", zap.Int("sequence", i), zap.String("payload", payload))
	}

	uncompressed, err := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + "-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + "-*.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	if len(uncompressed) == 0 || len(compressed) != 0 {
		t.Fatalf("drain window archives: uncompressed=%d compressed=%d", len(uncompressed), len(compressed))
	}

	forceGatewayArchiveCompression(t, runtime)
	if err := runtime.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	compressed, err = filepath.Glob(strings.TrimSuffix(path, ".jsonl") + "-*.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) == 0 {
		t.Fatalf("rotated gzip not found next to %s", path)
	}
}

func TestGatewayConfiguredRotationThresholds(t *testing.T) {
	if os.Getenv("GATEWAY_ROTATION_E2E") != "1" {
		t.Skip("set GATEWAY_ROTATION_E2E=1 to verify 25 MiB and 100 MiB rotation thresholds")
	}

	for _, test := range []struct {
		name      string
		maxSizeMB int
		records   int
	}{
		{name: "development", maxSizeMB: 25, records: 30},
		{name: "production", maxSizeMB: 100, records: 115},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, path := newTestGatewayRuntime(t, zapcore.InfoLevel, test.maxSizeMB)
			payload := strings.Repeat("x", 900<<10)
			for sequence := 0; sequence < test.records; sequence++ {
				Info(runtime.Logger, "system", "service", "rotation threshold probe",
					zap.Int("sequence", sequence),
					zap.String("payload", payload),
				)
			}
			forceGatewayArchiveCompression(t, runtime)
			if err := runtime.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			deadline := time.Now().Add(10 * time.Second)
			for {
				matches, err := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + "-*.jsonl.gz")
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("rotated gzip not found next to %s", path)
				}
				time.Sleep(25 * time.Millisecond)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() > int64(test.maxSizeMB)<<20 {
				t.Fatalf("active file size = %d, exceeds %d MiB", info.Size(), test.maxSizeMB)
			}
		})
	}
}

func forceGatewayArchiveCompression(t *testing.T, runtime *GatewayRuntime) {
	t.Helper()
	if err := runtime.archiver.compressReady(time.Now().Add(gatewayArchiveCompressionDelay + time.Second)); err != nil {
		t.Fatalf("compress gateway archives: %v", err)
	}
}

func newTestGatewayRuntime(t *testing.T, level zapcore.Level, maxSizeMB int) (*GatewayRuntime, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs", "gateway.jsonl")
	runtime, err := NewGateway(config.GatewayLogConfig{
		Environment:    config.GatewayEnvironmentTest,
		BaselineLevel:  level,
		ConsoleEnabled: false,
		FilePath:       path,
		MaxSizeMB:      maxSizeMB,
		MaxBackups:     20,
		MaxAgeDays:     14,
	}, "gw-test")
	if err != nil {
		t.Fatalf("new gateway logger: %v", err)
	}
	return runtime, path
}

func readGatewayRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []map[string]any
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), maxGatewayLogLineSize+1024)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("parse JSONL: %v\n%s", err, scanner.Text())
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}
