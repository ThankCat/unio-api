package p4cluster_test

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	redislib "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/bootstrap"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const redisClusterImage = "redis:7-alpine"

var errDatabaseReachedBeforeClusterGate = errors.New("database reached before Redis Cluster deployment gate")

func TestRedisClusterDeploymentIsRejected(t *testing.T) {
	if os.Getenv("P4_CLUSTER_E2E") != "1" {
		t.Skip("set P4_CLUSTER_E2E=1 to run the isolated Redis Cluster deployment drill")
	}

	cluster := startIsolatedRedisCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	assertClusterEnabled(t, ctx, cluster.client)

	t.Run("breaker_store_rejects_cluster", func(t *testing.T) {
		store := breakerstore.NewStore(cluster.client, cluster.namespace)
		err := store.VerifySingleNodeDeployment(ctx)
		assertUnsupportedClusterError(t, err)
	})

	t.Run("gateway_bootstrap_rejects_before_database_or_upstream", func(t *testing.T) {
		var upstreamCalls atomic.Int64
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()

		db := &rejectingGatewayDB{upstreamURL: upstream.URL}
		app, err := bootstrap.NewGatewayServerApp(ctx, bootstrap.GatewayServerAppDeps{
			Logger: zap.NewNop(),
			Config: config.Config{
				Redis: config.RedisConfig{KeyNamespace: cluster.namespace},
			},
			DB:    db,
			Redis: cluster.client,
		})
		assertUnsupportedClusterError(t, err)
		if app != nil {
			t.Fatal("gateway bootstrap returned an app for an unsupported Redis Cluster deployment")
		}
		if got := db.calls.Load(); got != 0 {
			t.Fatalf("gateway bootstrap accessed PostgreSQL before rejecting Redis Cluster: calls=%d", got)
		}
		if got := upstreamCalls.Load(); got != 0 {
			t.Fatalf("gateway bootstrap reached mock upstream after Redis Cluster rejection: calls=%d", got)
		}
	})

	t.Run("multi_key_lua_fails_crossslot_without_partial_writes", func(t *testing.T) {
		keyA, keyB := keysInDifferentSlots(t, ctx, cluster.client, cluster.namespace)
		script := `
			redis.call("SET", KEYS[1], ARGV[1])
			redis.call("SET", KEYS[2], ARGV[2])
			return "ok"
		`
		_, err := cluster.client.Eval(ctx, script, []string{keyA, keyB}, "value-a", "value-b").Result()
		if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CROSSSLOT") {
			t.Fatalf("multi-key Lua error=%v, want CROSSSLOT", err)
		}
		for _, key := range []string{keyA, keyB} {
			exists, existsErr := cluster.client.Exists(ctx, key).Result()
			if existsErr != nil {
				t.Fatalf("check key %q after CROSSSLOT: %v", key, existsErr)
			}
			if exists != 0 {
				t.Fatalf("multi-key Lua partially wrote key %q before CROSSSLOT", key)
			}
		}
	})
}

type isolatedRedisCluster struct {
	client    *redislib.Client
	namespace string
}

func startIsolatedRedisCluster(t *testing.T) *isolatedRedisCluster {
	t.Helper()
	requireDocker(t)

	suffix := randomSuffix(t)
	networkName := "unio-p4-cluster-net-" + suffix
	volumeName := "unio-p4-cluster-data-" + suffix
	containerName := "unio-p4-cluster-redis-" + suffix
	namespace := "unio:p4cluster:" + suffix

	runDocker(t, "network", "create", "--label", "unio.test=p4cluster", networkName)
	t.Cleanup(func() {
		runDockerCleanup(t, "network", "rm", networkName)
	})
	runDocker(t, "volume", "create", "--label", "unio.test=p4cluster", volumeName)
	t.Cleanup(func() {
		runDockerCleanup(t, "volume", "rm", "-f", volumeName)
	})

	t.Cleanup(func() {
		runDockerCleanupAllowMissing(t, "No such container", "rm", "--force", "--volumes", containerName)
	})
	runDocker(t,
		"run", "--detach",
		"--name", containerName,
		"--label", "unio.test=p4cluster",
		"--network", networkName,
		"--mount", "type=volume,source="+volumeName+",target=/data",
		"--publish", "127.0.0.1::6379",
		redisClusterImage,
		"redis-server",
		"--port", "6379",
		"--bind", "0.0.0.0",
		"--protected-mode", "no",
		"--appendonly", "no",
		"--cluster-enabled", "yes",
		"--cluster-config-file", "/data/nodes.conf",
		"--cluster-node-timeout", "5000",
	)

	portRaw := runDocker(t, "inspect", "--format", `{{(index (index .NetworkSettings.Ports "6379/tcp") 0).HostPort}}`, containerName)
	port, err := strconv.Atoi(strings.TrimSpace(portRaw))
	if err != nil || port <= 0 {
		t.Fatalf("parse Redis Cluster host port %q: %v", portRaw, err)
	}
	client := redislib.NewClient(&redislib.Options{
		Addr:         net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		MaxRetries:   0,
	})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Redis Cluster client: %v", err)
		}
	})
	waitForRedis(t, client, 20*time.Second)
	assignAllClusterSlots(t, client)
	waitForClusterOK(t, client, 20*time.Second)

	return &isolatedRedisCluster{client: client, namespace: namespace}
}

func requireDocker(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Docker is required for P4_CLUSTER_E2E: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func runDocker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func runDockerCleanup(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("cleanup docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func runDockerCleanupAllowMissing(t *testing.T, missingMessage string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	if output, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(output), missingMessage) {
		t.Errorf("cleanup docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	var raw [6]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		t.Fatalf("generate isolated Docker resource suffix: %v", err)
	}
	return hex.EncodeToString(raw[:])
}

func waitForRedis(t *testing.T, client *redislib.Client, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		lastErr = client.Ping(ctx).Err()
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Redis Cluster did not accept connections within %s: %v", timeout, lastErr)
}

func assignAllClusterSlots(t *testing.T, client *redislib.Client) {
	t.Helper()
	args := make([]interface{}, 0, 2+16384)
	args = append(args, "CLUSTER", "ADDSLOTS")
	for slot := 0; slot < 16384; slot++ {
		args = append(args, slot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Do(ctx, args...).Err(); err != nil {
		t.Fatalf("assign all slots to isolated Redis Cluster node: %v", err)
	}
}

func waitForClusterOK(t *testing.T, client *redislib.Client, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastInfo string
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		lastInfo, lastErr = client.ClusterInfo(ctx).Result()
		cancel()
		if lastErr == nil && infoField(lastInfo, "cluster_state") == "ok" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Redis Cluster did not become ready within %s: err=%v info=%q", timeout, lastErr, lastInfo)
}

func assertClusterEnabled(t *testing.T, ctx context.Context, client *redislib.Client) {
	t.Helper()
	info, err := client.Info(ctx, "cluster").Result()
	if err != nil {
		t.Fatalf("INFO cluster: %v", err)
	}
	if infoField(info, "cluster_enabled") != "1" {
		t.Fatalf("cluster_enabled=%q want=1; info=%q", infoField(info, "cluster_enabled"), info)
	}
	clusterInfo, err := client.ClusterInfo(ctx).Result()
	if err != nil {
		t.Fatalf("CLUSTER INFO: %v", err)
	}
	for field, want := range map[string]string{
		"cluster_state":          "ok",
		"cluster_slots_assigned": "16384",
		"cluster_known_nodes":    "1",
	} {
		if got := infoField(clusterInfo, field); got != want {
			t.Fatalf("%s=%q want=%q; cluster info=%q", field, got, want, clusterInfo)
		}
	}
}

func infoField(info, name string) string {
	for _, line := range strings.Split(strings.ReplaceAll(info, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && key == name {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func keysInDifferentSlots(
	t *testing.T,
	ctx context.Context,
	client *redislib.Client,
	namespace string,
) (string, string) {
	t.Helper()
	first := namespace + ":crossslot:{candidate-0}"
	firstSlot, err := client.ClusterKeySlot(ctx, first).Result()
	if err != nil {
		t.Fatalf("read cluster slot for %q: %v", first, err)
	}
	for index := 1; index < 100; index++ {
		candidate := fmt.Sprintf("%s:crossslot:{candidate-%d}", namespace, index)
		slot, slotErr := client.ClusterKeySlot(ctx, candidate).Result()
		if slotErr != nil {
			t.Fatalf("read cluster slot for %q: %v", candidate, slotErr)
		}
		if slot != firstSlot {
			return first, candidate
		}
	}
	t.Fatal("could not find two test keys in different Redis Cluster slots")
	return "", ""
}

func assertUnsupportedClusterError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected unsupported Redis Cluster error")
	}
	if got := failure.CodeOf(err); got != failure.CodeConfigUnsupported {
		t.Fatalf("Redis Cluster error code=%q want=%q err=%v", got, failure.CodeConfigUnsupported, err)
	}
	if !strings.Contains(err.Error(), "P4 does not support Redis Cluster") {
		t.Fatalf("Redis Cluster rejection is not explicit: %v", err)
	}
}

type rejectingGatewayDB struct {
	upstreamURL string
	calls       atomic.Int64
}

func (db *rejectingGatewayDB) reject() error {
	db.calls.Add(1)
	return fmt.Errorf("%w: configured upstream=%s", errDatabaseReachedBeforeClusterGate, db.upstreamURL)
}

func (db *rejectingGatewayDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, db.reject()
}

func (db *rejectingGatewayDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, db.reject()
}

func (db *rejectingGatewayDB) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return rejectingGatewayRow{err: db.reject()}
}

func (db *rejectingGatewayDB) Begin(context.Context) (pgx.Tx, error) {
	return nil, db.reject()
}

type rejectingGatewayRow struct {
	err error
}

func (row rejectingGatewayRow) Scan(...any) error {
	return row.err
}
