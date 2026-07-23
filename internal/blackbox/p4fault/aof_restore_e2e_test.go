package p4fault_test

import (
	"context"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	redislib "github.com/redis/go-redis/v9"
)

// TestP4AOFRestoreMaintenanceE2E restores an actual Redis 7 AOF archive from
// the previous ready epoch after PostgreSQL has durably entered recovering.
// Active long-stream permits are covered by the separate Redis outage drill;
// combining an in-flight permit with an epoch rollback remains separate coverage.
func TestP4AOFRestoreMaintenanceE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_AOF_RESTORE_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_AOF_RESTORE_E2E=1 to run the Redis AOF restore drill")
	}
	runStateLossMaintenanceE2E(t, setupFaultHarness(t), maintenanceAOFRestoreLoss)
}

type redisAOFSnapshot struct {
	backupVolume string
	archive      string
	manifest     string
	aofSegment   string
}

func archiveRedisAOF(t *testing.T, infra *isolatedInfra) redisAOFSnapshot {
	t.Helper()
	backupVolume := "unio-p4-fault-aof-backup-" + randomSuffix(t)
	const archive = "old-ready-aof.tar"
	runCommand(t, 20*time.Second, "docker", "volume", "create", backupVolume)
	t.Cleanup(func() {
		bestEffortCommand("docker", "volume", "rm", "-f", backupVolume)
	})

	runCommand(t, 20*time.Second, "docker", "pause", infra.redisContainer)
	paused := true
	defer func() {
		if paused {
			bestEffortCommand("docker", "unpause", infra.redisContainer)
		}
	}()
	runCommand(
		t,
		30*time.Second,
		"docker", "run", "--rm",
		"-v", infra.redisVolume+":/data:ro",
		"-v", backupVolume+":/backup",
		redisImage,
		"tar", "-C", "/data", "-cf", "/backup/"+archive, ".",
	)

	listing := runCommand(
		t,
		20*time.Second,
		"docker", "run", "--rm",
		"-v", backupVolume+":/backup:ro",
		redisImage,
		"tar", "-tf", "/backup/"+archive,
	)
	var manifest, segment string
	for _, entry := range strings.Fields(listing) {
		entry = strings.TrimPrefix(entry, "./")
		switch {
		case entry == "appendonlydir/appendonly.aof.manifest":
			manifest = entry
		case strings.HasPrefix(entry, "appendonlydir/") && strings.HasSuffix(entry, ".aof"):
			segment = entry
		}
	}
	if manifest == "" || segment == "" {
		t.Fatalf("Redis volume archive does not contain a real AOF manifest and segment: %s", listing)
	}

	runCommand(t, 20*time.Second, "docker", "unpause", infra.redisContainer)
	paused = false
	waitForRedis(t, infra.redisAddr, 20*time.Second)
	return redisAOFSnapshot{
		backupVolume: backupVolume,
		archive:      archive,
		manifest:     manifest,
		aofSegment:   segment,
	}
}

func restoreRedisAOF(t *testing.T, infra *isolatedInfra, snapshot redisAOFSnapshot) {
	t.Helper()
	if snapshot.backupVolume == "" || snapshot.archive == "" ||
		snapshot.manifest == "" || snapshot.aofSegment == "" {
		t.Fatal("cannot restore an incomplete Redis AOF snapshot")
	}

	infra.stopRedis(t)
	runCommand(
		t,
		20*time.Second,
		"docker", "run", "--rm",
		"-v", infra.redisVolume+":/data",
		redisImage,
		"sh", "-ec", "rm -rf /data/* /data/.[!.]* /data/..?*",
	)
	if remaining := runCommand(
		t,
		20*time.Second,
		"docker", "run", "--rm",
		"-v", infra.redisVolume+":/data:ro",
		redisImage,
		"sh", "-ec", "find /data -mindepth 1 -print -quit",
	); remaining != "" {
		t.Fatalf("Redis data volume was not empty before AOF restore: %s", remaining)
	}
	runCommand(
		t,
		30*time.Second,
		"docker", "run", "--rm",
		"-v", infra.redisVolume+":/data",
		"-v", snapshot.backupVolume+":/backup:ro",
		redisImage,
		"tar", "-C", "/data", "-xf", "/backup/"+snapshot.archive,
	)

	infra.startRedis(t)
	waitForRedis(t, infra.redisAddr, 20*time.Second)
	info := runCommand(
		t,
		20*time.Second,
		"docker", "exec", infra.redisContainer,
		"redis-cli", "--raw", "INFO", "persistence",
	)
	if !strings.Contains(info, "aof_enabled:1") {
		t.Fatalf("restored Redis did not load with AOF enabled: %s", info)
	}
}

func assertRedisHashRestored(
	t *testing.T,
	client *redislib.Client,
	key string,
	want map[string]string,
	stage string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := client.HGetAll(ctx, key).Result()
	if err != nil || !maps.Equal(got, want) {
		t.Fatalf("%s mismatch for %s: want=%v got=%v err=%v", stage, key, want, got, err)
	}
}
