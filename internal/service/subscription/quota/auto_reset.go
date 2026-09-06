package quota

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/accountusage"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// 自动使用重置卡（对齐 sub2api 的 auto reset credit，按本仓库契约重写；触发条件比它多一个「与」）：
//   - 账号级开关 + 5h/7d 各自阈值：留空的窗口不参与触发；多个窗口按 mode 组合——any 任一达到即用卡，
//     all 全部已设窗口同时达到才用卡。一张卡同时重置两个窗口，5h 几小时内自己就恢复，只因 5h 打满就用卡
//     会浪费周重置；但 7d 打满账号会被锁到下周——两种代价由运营按账号自己权衡；
//   - 只在实际用量达到阈值且账号持有可用卡时消费，选最早到期的一张；无卡或失败账号保持暂停；
//   - 一个「周期」= 两个窗口的重置时刻组合；同一周期内重试必须复用同一张卡与同一 redeem id
//     （上游幂等键），绝不因失败改选下一张卡把两张都烧掉；
//   - worker 单实例运行（DEVELOPMENT.md），不需要跨实例锁；状态机脱敏落库供管理端展示。

// 多窗口触发方式。
const (
	AutoResetModeAny = "any"
	AutoResetModeAll = "all"
)

// ValidAutoResetMode 判断触发方式取值是否合法。
func ValidAutoResetMode(mode string) bool {
	return mode == AutoResetModeAny || mode == AutoResetModeAll
}

// 自动用卡状态机取值。
const (
	AutoResetStatusChecking  = "checking"
	AutoResetStatusAvailable = "available"
	AutoResetStatusResetting = "resetting"
	AutoResetStatusSuccess   = "success"
	AutoResetStatusNoCredit  = "no_credit"
	AutoResetStatusFailed    = "failed"
)

// 自动用卡失败码。
const (
	AutoResetErrorQueryFailed        = "RESET_CREDIT_QUERY_FAILED"
	AutoResetErrorNoCredit           = "NO_RESET_CREDIT"
	AutoResetErrorDetailsIncomplete  = "RESET_CREDIT_DETAILS_INCOMPLETE"
	AutoResetErrorOriginalCreditGone = "ORIGINAL_CREDIT_UNAVAILABLE"
	AutoResetErrorConsumeFailed      = "RESET_CREDIT_CONSUME_FAILED"
)

// AutoResetState 是 subscription_accounts.auto_reset_credit_state 的持久化形态（脱敏：只存卡与周期的指纹）。
type AutoResetState struct {
	Status            string    `json:"status"`
	TriggerWindow     string    `json:"trigger_window,omitempty"`
	AvailableCount    int       `json:"available_count"`
	CheckedAt         time.Time `json:"checked_at,omitzero"`
	LastResultAt      time.Time `json:"last_result_at,omitzero"`
	ErrorCode         string    `json:"error_code,omitempty"`
	ErrorMessage      string    `json:"error_message,omitempty"`
	AttemptCycleHash  string    `json:"attempt_cycle_hash,omitempty"`
	AttemptCreditHash string    `json:"attempt_credit_hash,omitempty"`
}

// ParseAutoResetState 解析运行态列；空值或损坏返回 nil。
func ParseAutoResetState(raw []byte) *AutoResetState {
	if len(raw) == 0 {
		return nil
	}
	var state AutoResetState
	if err := json.Unmarshal(raw, &state); err != nil || state.Status == "" {
		return nil
	}
	return &state
}

// AutoResetQueries 是自动用卡 worker 所需的查询。
type AutoResetQueries interface {
	ListAutoResetCreditAccounts(ctx context.Context, limit int32) ([]sqlc.ListAutoResetCreditAccountsRow, error)
	UpdateAccountAutoResetCreditState(ctx context.Context, arg sqlc.UpdateAccountAutoResetCreditStateParams) error
}

// AutoResetOptions 是 worker 的运行参数。
type AutoResetOptions struct {
	// Interval 是扫描周期；默认 1 分钟。
	Interval time.Duration
	// SnapshotTTL 是本地用量快照的新鲜期：超过则主动查一次（也顺带补上暂停中账号的观测盲区）；默认 10 分钟。
	SnapshotTTL time.Duration
	// PageLimit 是单轮评估的账号上限；默认 200。
	PageLimit int32
}

func (o AutoResetOptions) withDefaults() AutoResetOptions {
	if o.Interval <= 0 {
		o.Interval = time.Minute
	}
	if o.SnapshotTTL <= 0 {
		o.SnapshotTTL = 10 * time.Minute
	}
	if o.PageLimit <= 0 {
		o.PageLimit = 200
	}
	return o
}

// AutoResetWorker 周期评估开启自动用卡的账号（实现 workers.Unit）。
type AutoResetWorker struct {
	queries AutoResetQueries
	service *Service
	logger  *zap.Logger
	options AutoResetOptions
	now     func() time.Time

	nextSweep time.Time
}

// NewAutoResetWorker 创建自动用卡 worker。
func NewAutoResetWorker(queries AutoResetQueries, service *Service, logger *zap.Logger, options AutoResetOptions) *AutoResetWorker {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AutoResetWorker{queries: queries, service: service, logger: logger, options: options.withDefaults(), now: time.Now}
}

// Name 实现 workers.Unit。
func (w *AutoResetWorker) Name() string { return "subscription_auto_reset_credit" }

// RunOnce 到达周期即评估一轮；未到周期直接返回。单个账号的失败只记入其运行态，不中断其余账号。
func (w *AutoResetWorker) RunOnce(ctx context.Context) (bool, error) {
	if w.now().Before(w.nextSweep) {
		return false, nil
	}
	w.nextSweep = w.now().Add(w.options.Interval)
	rows, err := w.queries.ListAutoResetCreditAccounts(ctx, w.options.PageLimit)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		w.evaluate(ctx, row)
	}
	return len(rows) > 0, nil
}

// thresholds 是账号的自动用卡触发条件：每个窗口的阈值（nil = 不参与）与多窗口组合方式。
type thresholds struct {
	fiveHour *int32
	sevenDay *int32
	mode     string
}

func resolveThresholds(mode string, fiveHour, sevenDay pgtype.Int4) thresholds {
	if !ValidAutoResetMode(mode) {
		mode = AutoResetModeAny
	}
	return thresholds{fiveHour: participatingThreshold(fiveHour), sevenDay: participatingThreshold(sevenDay), mode: mode}
}

func participatingThreshold(v pgtype.Int4) *int32 {
	if v.Valid && accountusage.ValidThreshold(v.Int32) {
		percent := v.Int32
		return &percent
	}
	return nil
}

// assessment 是一次阈值判定：参与触发的窗口各自是否达到了阈值，以及按 mode 组合后的结论。
type assessment struct {
	fiveHour bool
	sevenDay bool
	limits   thresholds
}

// reached 按 mode 组合：any 任一已设窗口达到；all 全部已设窗口都达到（没有参与窗口时永不触发）。
func (a assessment) reached() bool {
	participating := 0
	hit := 0
	if a.limits.fiveHour != nil {
		participating++
		if a.fiveHour {
			hit++
		}
	}
	if a.limits.sevenDay != nil {
		participating++
		if a.sevenDay {
			hit++
		}
	}
	if participating == 0 {
		return false
	}
	if a.limits.mode == AutoResetModeAll {
		return hit == participating
	}
	return hit > 0
}

// window 标出达到阈值的窗口（展示用；all 模式下只有全部达到才会真正触发）。
func (a assessment) window() string {
	switch {
	case a.fiveHour && a.sevenDay:
		return "5h+7d"
	case a.fiveHour:
		return "5h"
	case a.sevenDay:
		return "7d"
	default:
		return ""
	}
}

// assessSnapshot 用本地快照判定：窗口已重置（reset_at <= now）的高水位不算触顶。
func assessSnapshot(snapshot accountusage.Snapshot, limits thresholds, now time.Time) assessment {
	return assessment{
		fiveHour: windowReached(snapshot.Primary, limits.fiveHour, now),
		sevenDay: windowReached(snapshot.Secondary, limits.sevenDay, now),
		limits:   limits,
	}
}

func windowReached(window *accountusage.Window, threshold *int32, now time.Time) bool {
	return threshold != nil && window != nil && window.ResetAt > now.Unix() && window.UsedPercent >= float64(*threshold)
}

// assessUsage 用刚查到的上游用量判定。
func assessUsage(usage Usage, limits thresholds, now time.Time) assessment {
	result := assessment{limits: limits}
	if usage.RateLimit == nil {
		return result
	}
	result.fiveHour = upstreamWindowReached(usage.RateLimit.PrimaryWindow, limits.fiveHour, now)
	result.sevenDay = upstreamWindowReached(usage.RateLimit.SecondaryWindow, limits.sevenDay, now)
	return result
}

func upstreamWindowReached(window *Window, threshold *int32, now time.Time) bool {
	if threshold == nil || window == nil || window.UsedPercent < float64(*threshold) {
		return false
	}
	resetAt := window.ResetAt
	if resetAt <= 0 && window.ResetAfterSeconds > 0 {
		resetAt = now.Unix() + window.ResetAfterSeconds
	}
	return resetAt > now.Unix()
}

// cycleHash 标识「这一轮窗口」：两个窗口的重置时刻组合。窗口重置后 reset_at 变化即进入新周期。
func cycleHash(usage Usage) string {
	var primary, secondary int64
	if usage.RateLimit != nil {
		if usage.RateLimit.PrimaryWindow != nil {
			primary = usage.RateLimit.PrimaryWindow.ResetAt
		}
		if usage.RateLimit.SecondaryWindow != nil {
			secondary = usage.RateLimit.SecondaryWindow.ResetAt
		}
	}
	return shortHash(fmt.Sprintf("5h:%d|7d:%d", primary, secondary))
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

// redeemRequestID 由账号、卡指纹与周期指纹确定性派生：同一周期内对同一张卡的重试拿到同一个幂等键。
func redeemRequestID(accountID int64, creditHash, cycle string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("unio-auto-reset-credit:%d:%s:%s", accountID, creditHash, cycle))).String()
}

// pickCredit 选卡：同周期内已有尝试则必须复用原卡（找不到就拒绝换卡），否则取最早到期的可用卡。
func pickCredit(candidates []ResetCredit, previous *AutoResetState, cycle string) (ResetCredit, string, error) {
	if len(candidates) == 0 {
		return ResetCredit{}, AutoResetErrorDetailsIncomplete, fmt.Errorf("no usable reset credit details")
	}
	if previous != nil && previous.AttemptCycleHash == cycle && previous.AttemptCreditHash != "" {
		for _, candidate := range candidates {
			if candidate.ID != "" && shortHash(candidate.ID) == previous.AttemptCreditHash {
				return candidate, "", nil
			}
		}
		return ResetCredit{}, AutoResetErrorOriginalCreditGone, fmt.Errorf("the reset credit attempted earlier in this cycle is no longer listed; refusing to switch credits")
	}
	for _, candidate := range candidates {
		if candidate.ID != "" {
			return candidate, "", nil
		}
	}
	return ResetCredit{}, AutoResetErrorDetailsIncomplete, fmt.Errorf("usable reset credits have no id")
}

// evaluate 评估一个账号并推进其状态机。
func (w *AutoResetWorker) evaluate(ctx context.Context, row sqlc.ListAutoResetCreditAccountsRow) {
	now := w.now()
	limits := resolveThresholds(row.AutoResetCreditMode, row.AutoResetCredit5hThresholdPercent, row.AutoResetCredit7dThresholdPercent)
	previous := ParseAutoResetState(row.AutoResetCreditState)

	snapshot, hasSnapshot := accountusage.ParseSnapshot(row.UsageSnapshot)
	local := assessment{}
	if hasSnapshot {
		local = assessSnapshot(snapshot, limits, now)
	}
	stale := !hasSnapshot || snapshot.CapturedAt.IsZero() || now.Sub(snapshot.CapturedAt) > w.options.SnapshotTTL
	if !stale && !local.reached() {
		// 水位回落且上一轮还挂着触发窗口：清掉触发标记，让「自动重置成功」不会永远显示在列表上。
		if previous != nil && previous.TriggerWindow != "" && previous.Status != AutoResetStatusSuccess {
			cleared := *previous
			cleared.TriggerWindow = ""
			cleared.ErrorCode = ""
			cleared.ErrorMessage = ""
			cleared.CheckedAt = now
			if cleared.AvailableCount > 0 {
				cleared.Status = AutoResetStatusAvailable
			} else {
				cleared.Status = AutoResetStatusNoCredit
			}
			w.persist(ctx, row.ID, cleared)
		}
		return
	}

	checking := AutoResetState{Status: AutoResetStatusChecking, TriggerWindow: local.window(), CheckedAt: now}
	if previous != nil {
		checking.AvailableCount = previous.AvailableCount
		checking.AttemptCycleHash = previous.AttemptCycleHash
		checking.AttemptCreditHash = previous.AttemptCreditHash
	}
	w.persist(ctx, row.ID, checking)

	report, err := w.service.QueryUsage(ctx, row.ID)
	if err != nil {
		w.fail(ctx, row.ID, checking, AutoResetErrorQueryFailed, err)
		return
	}
	fresh := assessUsage(report.Usage, limits, now)
	available := report.Credits.AvailableCount
	if !fresh.reached() {
		status := AutoResetStatusNoCredit
		if available > 0 {
			status = AutoResetStatusAvailable
		}
		w.persist(ctx, row.ID, AutoResetState{Status: status, AvailableCount: available, CheckedAt: now})
		return
	}
	if available <= 0 {
		w.persist(ctx, row.ID, AutoResetState{
			Status: AutoResetStatusNoCredit, TriggerWindow: fresh.window(), CheckedAt: now, LastResultAt: now,
			ErrorCode: AutoResetErrorNoCredit,
		})
		return
	}

	cycle := cycleHash(report.Usage)
	credit, code, pickErr := pickCredit(report.UsableCredits(), previous, cycle)
	if pickErr != nil {
		failed := checking
		failed.TriggerWindow = fresh.window()
		failed.AvailableCount = available
		failed.AttemptCycleHash = cycle
		w.fail(ctx, row.ID, failed, code, pickErr)
		return
	}
	creditHash := shortHash(credit.ID)
	resetting := AutoResetState{
		Status: AutoResetStatusResetting, TriggerWindow: fresh.window(), AvailableCount: available,
		CheckedAt: now, AttemptCycleHash: cycle, AttemptCreditHash: creditHash,
	}
	w.persist(ctx, row.ID, resetting)

	result, err := w.service.ConsumeTargeted(ctx, row.ID, credit.ID, redeemRequestID(row.ID, creditHash, cycle))
	if err != nil {
		w.fail(ctx, row.ID, resetting, AutoResetErrorConsumeFailed, err)
		return
	}
	if result.NoCredit() {
		w.persist(ctx, row.ID, AutoResetState{
			Status: AutoResetStatusNoCredit, TriggerWindow: fresh.window(), CheckedAt: now, LastResultAt: w.now(),
			ErrorCode: AutoResetErrorNoCredit, AttemptCycleHash: cycle, AttemptCreditHash: creditHash,
		})
		return
	}

	// 消费成功：回读用量（观测链路按新水位解除暂停）与卡数。回读失败不影响「已重置」这个结论。
	success := AutoResetState{
		Status: AutoResetStatusSuccess, TriggerWindow: fresh.window(), AvailableCount: max(0, available-1),
		CheckedAt: now, LastResultAt: w.now(), AttemptCycleHash: cycle, AttemptCreditHash: creditHash,
	}
	if refreshed, refreshErr := w.service.QueryUsage(ctx, row.ID); refreshErr == nil {
		success.AvailableCount = refreshed.Credits.AvailableCount
	} else {
		success.ErrorMessage = "回读用量失败：" + refreshErr.Error()
	}
	w.persist(ctx, row.ID, success)
	logging.Info(w.logger, "runtime", "account", "codex reset credit auto consumed",
		zap.Int64("account_id", row.ID),
		zap.String("display_name", row.DisplayName),
		zap.String("trigger_window", fresh.window()),
		zap.String("mode", limits.mode),
		zap.Int32p("threshold_5h", limits.fiveHour),
		zap.Int32p("threshold_7d", limits.sevenDay),
		zap.Int("windows_reset", result.WindowsReset),
		zap.Int("available_after", success.AvailableCount),
	)
}

// fail 记录失败态：保留尝试指纹，让下一轮在同周期内复用同一张卡与幂等键。
func (w *AutoResetWorker) fail(ctx context.Context, accountID int64, base AutoResetState, code string, err error) {
	failed := base
	failed.Status = AutoResetStatusFailed
	failed.ErrorCode = code
	failed.ErrorMessage = truncate(err.Error(), maxErrorBodyBytes)
	failed.LastResultAt = w.now()
	w.persist(ctx, accountID, failed)
	logging.Warn(w.logger, "runtime", "account", "codex reset credit auto reset failed",
		zap.Int64("account_id", accountID),
		zap.String("error_code", code),
		zap.String("error_message", err.Error()),
	)
}

func (w *AutoResetWorker) persist(ctx context.Context, accountID int64, state AutoResetState) {
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	if err := w.queries.UpdateAccountAutoResetCreditState(ctx, sqlc.UpdateAccountAutoResetCreditStateParams{
		ID: accountID, AutoResetCreditState: raw,
	}); err != nil {
		logging.Warn(w.logger, "runtime", "account", "persist auto reset credit state failed",
			zap.Int64("account_id", accountID), zap.String("error_message", err.Error()))
	}
}
