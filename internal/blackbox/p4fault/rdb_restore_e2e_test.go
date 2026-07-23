package p4fault_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestP4RDBRestoreMaintenanceE2E restores only a real Redis 7 dump.rdb from
// the previous ready epoch after PostgreSQL has durably entered recovering.
func TestP4RDBRestoreMaintenanceE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_RDB_RESTORE_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_RDB_RESTORE_E2E=1 to run the Redis RDB restore drill")
	}
	runStateLossMaintenanceE2E(t, setupFaultHarness(t), maintenanceRDBRestoreLoss)
}

type redisRDBSnapshot struct {
	backupVolume string
	file         string
	checksum     string
}

func archiveRedisRDB(t *testing.T, infra *isolatedInfra) redisRDBSnapshot {
	t.Helper()
	backupVolume := "unio-p4-fault-rdb-backup-" + randomSuffix(t)
	const snapshotFile = "old-ready-dump.rdb"
	runCommand(t, 20*time.Second, "docker", "volume", "create", backupVolume)
	t.Cleanup(func() {
		bestEffortCommand("docker", "volume", "rm", "-f", backupVolume)
	})

	if result := runCommand(
		t,
		30*time.Second,
		"docker", "exec", infra.redisContainer,
		"redis-cli", "--raw", "SAVE",
	); result != "OK" {
		t.Fatalf("Redis SAVE returned %q, want OK", result)
	}
	persistenceInfo := runCommand(
		t,
		20*time.Second,
		"docker", "exec", infra.redisContainer,
		"redis-cli", "--raw", "INFO", "persistence",
	)
	if !strings.Contains(persistenceInfo, "rdb_last_bgsave_status:ok") {
		t.Fatalf("Redis did not report a successful RDB save: %s", persistenceInfo)
	}

	runCommand(t, 20*time.Second, "docker", "pause", infra.redisContainer)
	paused := true
	defer func() {
		if paused {
			bestEffortCommand("docker", "unpause", infra.redisContainer)
		}
	}()
	runCommand(
		t,
		20*time.Second,
		"docker", "run", "--rm",
		"-v", infra.redisVolume+":/data:ro",
		"-v", backupVolume+":/backup",
		redisImage,
		"cp", "/data/dump.rdb", "/backup/"+snapshotFile,
	)
	runCommand(
		t,
		20*time.Second,
		"docker", "run", "--rm",
		"-v", backupVolume+":/backup:ro",
		redisImage,
		"redis-check-rdb", "/backup/"+snapshotFile,
	)
	checksum := redisVolumeFileChecksum(t, backupVolume, "/backup/"+snapshotFile)

	runCommand(t, 20*time.Second, "docker", "unpause", infra.redisContainer)
	paused = false
	waitForRedis(t, infra.redisAddr, 20*time.Second)
	return redisRDBSnapshot{
		backupVolume: backupVolume,
		file:         snapshotFile,
		checksum:     checksum,
	}
}

func restoreRedisRDB(t *testing.T, infra *isolatedInfra, snapshot redisRDBSnapshot) {
	t.Helper()
	if snapshot.backupVolume == "" || snapshot.file == "" || snapshot.checksum == "" {
		t.Fatal("cannot restore an incomplete Redis RDB snapshot")
	}

	infra.stopRedis(t)
	runCommand(t, 20*time.Second, "docker", "rm", infra.redisContainer)
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
		t.Fatalf("Redis data volume was not empty before RDB restore: %s", remaining)
	}
	runCommand(
		t,
		20*time.Second,
		"docker", "run", "--rm",
		"-v", infra.redisVolume+":/data",
		"-v", snapshot.backupVolume+":/backup:ro",
		redisImage,
		"cp", "/backup/"+snapshot.file, "/data/dump.rdb",
	)
	if checksum := redisVolumeFileChecksum(t, infra.redisVolume, "/data/dump.rdb"); checksum != snapshot.checksum {
		t.Fatalf("restored RDB checksum=%s want=%s", checksum, snapshot.checksum)
	}
	if listing := runCommand(
		t,
		20*time.Second,
		"docker", "run", "--rm",
		"-v", infra.redisVolume+":/data:ro",
		redisImage,
		"sh", "-ec", "find /data -mindepth 1 -maxdepth 1 -print",
	); listing != "/data/dump.rdb" {
		t.Fatalf("RDB restore wrote files other than dump.rdb: %s", listing)
	}

	runCommand(t, 2*time.Minute, "docker", "run", "-d",
		"--name", infra.redisContainer,
		"-p", "127.0.0.1:"+strconv.Itoa(infra.redisPort)+":6379",
		"-v", infra.redisVolume+":/data",
		redisImage,
		"redis-server", "--appendonly", "no", "--save", "",
	)
	waitForRedis(t, infra.redisAddr, 20*time.Second)
	persistenceInfo := runCommand(
		t,
		20*time.Second,
		"docker", "exec", infra.redisContainer,
		"redis-cli", "--raw", "INFO", "persistence",
	)
	if !strings.Contains(persistenceInfo, "aof_enabled:0") || !strings.Contains(persistenceInfo, "loading:0") {
		t.Fatalf("restored Redis did not finish loading in RDB-only mode: %s", persistenceInfo)
	}
	if checksum := redisVolumeFileChecksum(t, infra.redisVolume, "/data/dump.rdb"); checksum != snapshot.checksum {
		t.Fatalf("loaded RDB checksum=%s want=%s", checksum, snapshot.checksum)
	}
}

func redisVolumeFileChecksum(t *testing.T, volume, path string) string {
	t.Helper()
	output := runCommand(
		t,
		20*time.Second,
		"docker", "run", "--rm",
		"-v", volume+":"+strings.TrimSuffix(path, "/"+filepath.Base(path))+":ro",
		redisImage,
		"sha256sum", path,
	)
	checksum, _, ok := strings.Cut(output, " ")
	if !ok || len(checksum) != 64 {
		t.Fatalf("invalid checksum output for %s: %q", path, output)
	}
	return checksum
}
