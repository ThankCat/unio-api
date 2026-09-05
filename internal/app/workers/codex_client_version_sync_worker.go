package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/codexidentity"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const (
	// codexClientVersionSyncInterval：官方客户端是天级发版，6 小时足够及时，且把 GitHub 匿名 API
	// 的调用压到每天 4 次（匿名限额 60 次/小时）。
	codexClientVersionSyncInterval = 6 * time.Hour
	// codexClientVersionSyncRetry 是单次同步失败后的重试间隔（网络抖动不必等满一个周期）。
	codexClientVersionSyncRetry = 30 * time.Minute
	// codexClientVersionSyncTimeout 是单次同步整体超时。
	codexClientVersionSyncTimeout = 30 * time.Second
	// codexReleasesAPI 是官方 Codex 客户端仓库的 releases 接口；/latest 本身已排除 draft 与 prerelease。
	codexReleasesAPI = "https://api.github.com/repos/openai/codex/releases"
	// codexReleaseTagPrefix 是客户端 release 的 tag 前缀（如 rust-v0.152.1）。同仓库还有其他组件的 tag，
	// 必须按前缀过滤，否则会同步到无关版本号。
	codexReleaseTagPrefix = "rust-v"
	// codexReleasesListPageSize 是 /latest 不是客户端 tag 时的回退列表页大小。该仓库预发布极密集，
	// 30 条里可能只有一两条正式版，页大小不能再小。
	codexReleasesListPageSize = 30
	codexClientVersionSource  = "github:openai/codex"
)

// CodexClientVersionSyncWorker 周期性把官方 Codex 客户端最新正式版的版本号写入
// gateway.codex_client_version_synced，供各进程的 Codex 出站身份读取（Admin 未覆写时即以该版本出站）。
//
// 上游 /backend-api/codex 按客户端身份分优先级降载，陈旧版本先被丢弃；自动同步让运维不必为跟版本发版。
// 本 worker 是该设置项的唯一写入方；Admin 覆写值（gateway.codex_client_version）优先级更高，
// 因此手工固定版本不会被同步覆盖。
type CodexClientVersionSyncWorker struct {
	client   *http.Client
	settings *appsettings.SettingsStore
	logger   *zap.Logger
	now      func() time.Time
	endpoint string

	nextRunAt time.Time
}

// NewCodexClientVersionSyncWorker 创建版本同步 worker。client 为 nil 时用 http.DefaultClient。
func NewCodexClientVersionSyncWorker(client *http.Client, settings *appsettings.SettingsStore, logger *zap.Logger) *CodexClientVersionSyncWorker {
	if settings == nil {
		panic("workers: codex client version sync settings store is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CodexClientVersionSyncWorker{
		client:   client,
		settings: settings,
		logger:   logger,
		now:      time.Now,
		endpoint: codexReleasesAPI,
	}
}

// Name 返回 worker 名称。
func (w *CodexClientVersionSyncWorker) Name() string { return "codex_client_version_sync" }

// RunOnce 到期时同步一次；自动同步关闭时静默跳过（不算做了工作）。
//
// 进程首轮以持久化快照的 synced_at 为准：快照仍在周期内就顺延到 synced_at + 周期，而不是每次启动都
// 打一次 GitHub——热加载/滚动重启频繁时，匿名限额（60 次/小时）会被启动同步耗光。
func (w *CodexClientVersionSyncWorker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()
	if w.nextRunAt.IsZero() {
		if synced, err := appsettings.DecodeCodexClientVersionSynced(w.settings.Raw(ctx, appsettings.GatewayCodexClientVersionSyncedKey)); err == nil &&
			synced.Version != "" && now.Sub(synced.SyncedAt) < codexClientVersionSyncInterval {
			w.nextRunAt = synced.SyncedAt.Add(codexClientVersionSyncInterval)
			return false, nil
		}
	}
	if now.Before(w.nextRunAt) {
		return false, nil
	}
	if !w.autoSyncEnabled(ctx) {
		w.nextRunAt = now.Add(codexClientVersionSyncInterval)
		return false, nil
	}

	syncCtx, cancel := context.WithTimeout(ctx, codexClientVersionSyncTimeout)
	defer cancel()
	version, err := w.fetchLatestStableVersion(syncCtx)
	if err != nil {
		w.nextRunAt = now.Add(codexClientVersionSyncRetry)
		w.logger.Warn("codex client version sync failed; keeping previous value",
			zap.String("retry_in", codexClientVersionSyncRetry.String()),
			zap.Error(err),
		)
		return true, nil
	}
	w.nextRunAt = now.Add(codexClientVersionSyncInterval)

	previous, _ := appsettings.DecodeCodexClientVersionSynced(w.settings.Raw(ctx, appsettings.GatewayCodexClientVersionSyncedKey))
	snapshot := appsettings.CodexClientVersionSynced{Version: version, SyncedAt: now.UTC(), Source: codexClientVersionSource}
	if err := w.settings.Set(ctx, appsettings.GatewayCodexClientVersionSyncedKey, appsettings.EncodeCodexClientVersionSynced(snapshot)); err != nil {
		return true, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("persist codex client version snapshot"))
	}
	if previous.Version != version {
		w.logger.Info("codex client version synced",
			zap.String("previous", previous.Version),
			zap.String("version", version),
			zap.String("effective", codexidentity.FloorVersion(version)),
		)
	}
	return true, nil
}

func (w *CodexClientVersionSyncWorker) autoSyncEnabled(ctx context.Context) bool {
	raw := w.settings.Raw(ctx, appsettings.GatewayCodexClientVersionAutoSyncKey)
	if len(raw) == 0 {
		return true
	}
	enabled := true
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return true
	}
	return enabled
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// fetchLatestStableVersion 主路径读 /releases/latest（GitHub 已排除 draft/prerelease）；
// 若最新正式发布不是客户端 tag（同仓库其他组件），回退翻最近一页找首个 rust-v 正式版。
func (w *CodexClientVersionSyncWorker) fetchLatestStableVersion(ctx context.Context) (string, error) {
	var latest githubRelease
	if err := w.getJSON(ctx, w.endpoint+"/latest", &latest); err != nil {
		return "", err
	}
	if version, ok := codexVersionFromRelease(latest); ok {
		return version, nil
	}
	var releases []githubRelease
	if err := w.getJSON(ctx, fmt.Sprintf("%s?per_page=%d", w.endpoint, codexReleasesListPageSize), &releases); err != nil {
		return "", err
	}
	for _, release := range releases {
		if version, ok := codexVersionFromRelease(release); ok {
			return version, nil
		}
	}
	return "", errors.New("no stable codex client release found in the latest page")
}

func codexVersionFromRelease(release githubRelease) (string, bool) {
	if release.Draft || release.Prerelease {
		return "", false
	}
	tag := strings.TrimSpace(release.TagName)
	if !strings.HasPrefix(tag, codexReleaseTagPrefix) {
		return "", false
	}
	version := codexidentity.NormalizeVersion(strings.TrimPrefix(tag, codexReleaseTagPrefix))
	if version == "" {
		return "", false
	}
	// 正式发布不应带预发布后缀；带后缀说明 tag 语义变了，宁可跳过。
	if strings.Contains(version, "-") {
		return "", false
	}
	return version, true
}

func (w *CodexClientVersionSyncWorker) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "unio-gateway-codex-version-sync")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("github releases api returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
