// Package cdkey implements the authenticated Admin CDKEY management surface.
package cdkey

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	corecdkey "github.com/ThankCat/unio-gateway/internal/core/cdkey"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

const (
	statusUnused   = corecdkey.StatusUnused
	statusRedeemed = corecdkey.StatusRedeemed
	statusRevoked  = corecdkey.StatusRevoked
)

// TxBeginner is the minimal database transaction capability required by the service.
type TxBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Store captures generated queries used by Admin CDKEY operations.
type Store interface {
	CreateCDKey(context.Context, sqlc.CreateCDKeyParams) (sqlc.Cdkey, error)
	ListCDKeyIDs(context.Context, sqlc.ListCDKeyIDsParams) ([]int64, error)
	ListCDKeysPage(context.Context, sqlc.ListCDKeysPageParams) ([]sqlc.ListCDKeysPageRow, error)
	CountCDKeys(context.Context, sqlc.CountCDKeysParams) (int64, error)
	GetCDKeySummary(context.Context, sqlc.GetCDKeySummaryParams) ([]sqlc.GetCDKeySummaryRow, error)
	CountCDKeyBatches(context.Context, sqlc.CountCDKeyBatchesParams) (int64, error)
	ListCDKeyRedemptionsPage(context.Context, sqlc.ListCDKeyRedemptionsPageParams) ([]sqlc.ListCDKeyRedemptionsPageRow, error)
	CountCDKeyRedemptions(context.Context, sqlc.CountCDKeyRedemptionsParams) (int64, error)
	GetCDKeyByIDForUpdate(context.Context, int64) (sqlc.Cdkey, error)
	GetCDKeysForUpdateByIDs(context.Context, []int64) ([]sqlc.Cdkey, error)
	RevokeCDKeyIfUnused(context.Context, int64) (int64, error)
	DeleteCDKey(context.Context, int64) (int64, error)
	ExportCDKeysByIDs(context.Context, sqlc.ExportCDKeysByIDsParams) ([]sqlc.ExportCDKeysByIDsRow, error)
	ExportCDKeysByFilter(context.Context, sqlc.ExportCDKeysByFilterParams) ([]sqlc.ExportCDKeysByFilterRow, error)
}

// TxStoreFactory is an optional test/adapter hook for stores that can expose
// the same query surface on a caller-provided transaction. The generated
// *sqlc.Queries path remains the default and needs no adapter implementation.
type TxStoreFactory interface {
	StoreForTx(pgx.Tx) Store
}

// Service owns Admin CDKEY state transitions.
type Service struct {
	db    TxBeginner
	store Store
}

func NewService(db TxBeginner, store Store) *Service {
	if db == nil || store == nil {
		panic("cdkey admin service requires db and store")
	}
	return &Service{db: db, store: store}
}

// ListParams controls the masked CDKEY list.
type ListParams struct {
	Statuses []string
	Amount   string
	BatchID  string
	Search   string
	From     time.Time
	To       time.Time
	Sort     string
	Desc     bool
	Limit    int32
	Offset   int32
}

// CDKey is safe for normal JSON responses; plaintext/hash are deliberately absent.
type CDKey struct {
	ID                        int64
	BatchID                   string
	MaskedCode                string
	CodePrefix                string
	CodeSuffix                string
	Amount                    string
	Currency                  string
	Status                    string
	CreatedAt                 time.Time
	RedeemedAt                *time.Time
	RevokedAt                 *time.Time
	RedemptionID              *int64
	RedemptionUserID          *int64
	RedemptionUserEmail       *string
	RedemptionUserDisplayName *string
	RedemptionLedgerID        *int64
	RedemptionAt              *time.Time
}

// ListResult is the paginated masked list.
type ListResult struct {
	Items []CDKey
	Total int64
}

// GenerateParams controls one batch generation.
type GenerateParams struct {
	Items []GenerateItem
}

type GenerateItem struct {
	Amount   string
	Quantity int
}

// GenerateLine reports one denomination subtotal in a generated batch.
type GenerateLine struct {
	Amount   string `json:"amount"`
	Quantity int    `json:"quantity"`
	Value    string `json:"value"`
}

type GenerateResult struct {
	BatchID       string
	Currency      string
	TotalQuantity int
	TotalValue    string
	Lines         []GenerateLine
	MaskedCodes   []string
}

// Redemption is an Admin redemption fact view.
type Redemption struct {
	ID              int64
	CDKeyID         int64
	BatchID         string
	MaskedCode      string
	UserID          int64
	UserDisplayName string
	UserEmail       string
	Amount          string
	Currency        string
	LedgerEntryID   int64
	RedeemedAt      time.Time
}

type RedemptionsResult struct {
	Items []Redemption
	Total int64
}

// Summary contains fixed-denomination totals. Revoked rows are excluded from
// value/quantity/rate and exposed separately for operational visibility.
type Summary struct {
	Value          SummaryTotals
	Quantity       SummaryTotals
	RedemptionRate float64
	// BatchCount is the number of batches represented by non-revoked rows.
	// Keep the original field for API compatibility; the additional fields
	// provide the detail required by the batch-count card.
	BatchCount              int64
	BatchesWithUnused       int64
	FullyRedeemedBatchCount int64
	BatchByAmount           map[string]BatchSummaryAmount
	RevokedCount            int64
}

type SummaryTotals struct {
	Total    string
	Redeemed string
	Unused   string
	ByAmount map[string]SummaryAmount
}

type SummaryAmount struct {
	Redeemed      string
	Unused        string
	RedeemedCount int64
	UnusedCount   int64
}

// BatchSummaryAmount contains batch-level state counts for one denomination.
// A batch is counted as fully redeemed only when all of its non-revoked keys
// are redeemed.
type BatchSummaryAmount struct {
	BatchCount              int64
	BatchesWithUnused       int64
	FullyRedeemedBatchCount int64
}

// ExportParams selects statuses and either explicit IDs or all records.
type ExportParams struct {
	Statuses []string
	Scope    string
	IDs      []int64
}

// Selection describes either an explicit set of rows or a server-side filter.
// ExcludeIDs is used by the "select all filtered, then uncheck a few" flow.
type Selection struct {
	Scope      string
	IDs        []int64
	Filter     ListParams
	ExcludeIDs []int64
}

// ExportRow intentionally contains plaintext and must only be consumed by the
// authenticated CSV writer in app/adminapi/cdkey.
type ExportRow struct {
	ID                  int64
	BatchID             string
	CodePlaintext       string
	Amount              string
	Currency            string
	Status              string
	CreatedAt           time.Time
	RedeemedAt          *time.Time
	RevokedAt           *time.Time
	RedemptionUserID    *int64
	RedemptionUserEmail *string
	RedemptionLedgerID  *int64
	RedemptionAt        *time.Time
}

// List returns masked rows and never selects code_plaintext.
func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	statuses, err := normalizeStatuses(params.Statuses)
	if err != nil {
		return ListResult{}, err
	}
	amount, err := amountArg(params.Amount)
	if err != nil {
		return ListResult{}, err
	}
	batchID, err := uuidArg(params.BatchID)
	if err != nil {
		return ListResult{}, err
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}
	rows, err := s.store.ListCDKeysPage(ctx, sqlc.ListCDKeysPageParams{
		Statuses: statuses, Amount: amount, BatchID: batchID, Search: opsutil.TextNarg(strings.TrimSpace(params.Search)),
		FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To),
		SortField: opsutil.TextNarg(params.Sort), SortDesc: opsutil.BoolNarg(params.Desc),
		PageLimit: params.Limit, PageOffset: params.Offset,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list CDKEYs")
	}
	total := int64(0)
	if len(rows) > 0 {
		total = rows[0].TotalCount
	} else {
		total, err = s.store.CountCDKeys(ctx, sqlc.CountCDKeysParams{
			Statuses: statuses, Amount: amount, BatchID: batchID, Search: opsutil.TextNarg(strings.TrimSpace(params.Search)),
			FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To),
		})
		if err != nil {
			return ListResult{}, storeFailed(err, "count CDKEYs")
		}
	}
	items := make([]CDKey, 0, len(rows))
	for _, row := range rows {
		items = append(items, cdkeyFromListRow(row))
	}
	return ListResult{Items: items, Total: total}, nil
}

// Generate creates one atomic multi-denomination batch. Plaintext is retained
// only in the DB and is not part of GenerateResult.
func (s *Service) Generate(ctx context.Context, params GenerateParams) (GenerateResult, error) {
	if len(params.Items) == 0 {
		return GenerateResult{}, invalidArgument("items", "at least one denomination is required")
	}
	items := make([]GenerateItem, 0, len(params.Items))
	seenAmounts := make(map[string]struct{}, len(params.Items))
	totalQuantity := 0
	for index, item := range params.Items {
		amountText, ok := corecdkey.AmountString(item.Amount)
		if !ok {
			return GenerateResult{}, invalidArgument("items", fmt.Sprintf("items[%d].amount must be one of 5, 10, 30, 50, 100, 200, 500 USD", index))
		}
		if _, exists := seenAmounts[amountText]; exists {
			return GenerateResult{}, invalidArgument("items", fmt.Sprintf("items[%d].amount is duplicated", index))
		}
		if item.Quantity < 1 {
			return GenerateResult{}, invalidArgument("items", fmt.Sprintf("items[%d].quantity must be a positive integer", index))
		}
		seenAmounts[amountText] = struct{}{}
		items = append(items, GenerateItem{Amount: amountText, Quantity: item.Quantity})
		totalQuantity += item.Quantity
		if totalQuantity > corecdkey.MaxQuantity {
			return GenerateResult{}, invalidArgument("items", fmt.Sprintf("total quantity must not exceed %d", corecdkey.MaxQuantity))
		}
	}
	batchID := uuid.New()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return GenerateResult{}, storeFailed(err, "begin CDKEY generation transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q, queryErr := s.storeForTx(tx)
	if queryErr != nil {
		return GenerateResult{}, queryErr
	}
	masked := make([]string, 0, totalQuantity)
	lines := make([]GenerateLine, 0, len(items))
	seen := make(map[string]struct{}, totalQuantity)
	for _, item := range items {
		amount, _ := corecdkey.AmountNumeric(item.Amount)
		for generated := 0; generated < item.Quantity; {
			plain, genErr := corecdkey.Generate()
			if genErr != nil {
				return GenerateResult{}, failure.Wrap(failure.CodeAdminStoreFailed, genErr, failure.WithMessage("generate CDKEY"))
			}
			hash := corecdkey.Hash(plain)
			if _, exists := seen[hash]; exists {
				continue
			}
			seen[hash] = struct{}{}
			prefix, suffix := corecdkey.PrefixSuffix(plain)
			row, createErr := q.CreateCDKey(ctx, sqlc.CreateCDKeyParams{
				BatchID: pgUUID(batchID), CodePlaintext: plain, CodeHash: hash,
				CodePrefix: prefix, CodeSuffix: suffix, Amount: amount,
				Currency: corecdkey.Currency, Status: corecdkey.StatusUnused,
			})
			if createErr != nil {
				return GenerateResult{}, storeFailed(createErr, "store generated CDKEY")
			}
			masked = append(masked, corecdkey.Mask(row.CodePlaintext))
			generated++
		}
		lines = append(lines, GenerateLine{Amount: item.Amount, Quantity: item.Quantity, Value: multiplyAmount(item.Amount, item.Quantity)})
	}
	if err := tx.Commit(ctx); err != nil {
		return GenerateResult{}, storeFailed(err, "commit CDKEY generation transaction")
	}
	totalValue := ""
	for _, line := range lines {
		totalValue = addDecimal(totalValue, line.Value)
	}
	return GenerateResult{BatchID: batchID.String(), Currency: corecdkey.Currency, TotalQuantity: totalQuantity, TotalValue: totalValue, Lines: lines, MaskedCodes: masked}, nil
}

// Summary computes the four dashboard cards under the supplied filters.
func (s *Service) Summary(ctx context.Context, params ListParams) (Summary, error) {
	requestedStatuses, err := normalizeStatuses(params.Statuses)
	if err != nil {
		return Summary{}, err
	}
	includeRevoked := len(requestedStatuses) == 0
	for _, status := range requestedStatuses {
		if status == statusRevoked {
			includeRevoked = true
			break
		}
	}
	statuses := nonRevokedStatuses(requestedStatuses)
	if len(requestedStatuses) == 0 {
		statuses = []string{statusUnused, statusRedeemed}
	}
	amount, err := amountArg(params.Amount)
	if err != nil {
		return Summary{}, err
	}
	batchID, err := uuidArg(params.BatchID)
	if err != nil {
		return Summary{}, err
	}
	result := Summary{
		Value:         SummaryTotals{ByAmount: make(map[string]SummaryAmount)},
		Quantity:      SummaryTotals{ByAmount: make(map[string]SummaryAmount)},
		BatchByAmount: make(map[string]BatchSummaryAmount),
	}
	var redeemedCount, unusedCount int64
	if len(statuses) > 0 {
		rows, summaryErr := s.store.GetCDKeySummary(ctx, sqlc.GetCDKeySummaryParams{
			Statuses: statuses, Amount: amount, BatchID: batchID,
			Search:   opsutil.TextNarg(strings.TrimSpace(params.Search)),
			FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To),
		})
		if summaryErr != nil {
			return Summary{}, storeFailed(summaryErr, "summarize CDKEYs")
		}
		for _, row := range rows {
			key := opsutil.NumericString(row.Amount)
			entry := result.Value.ByAmount[key]
			counts := result.Quantity.ByAmount[key]
			switch row.Status {
			case statusRedeemed:
				entry.Redeemed = opsutil.NumericString(row.TotalValue)
				counts.RedeemedCount = row.Quantity
				redeemedCount += row.Quantity
			case statusUnused:
				entry.Unused = opsutil.NumericString(row.TotalValue)
				counts.UnusedCount = row.Quantity
				unusedCount += row.Quantity
			}
			result.Value.ByAmount[key] = entry
			result.Quantity.ByAmount[key] = counts
		}
	}
	result.Value.Total = sumSummaryValues(result.Value.ByAmount, true)
	result.Value.Redeemed = sumSummaryValues(result.Value.ByAmount, false)
	result.Value.Unused = sumSummaryValues(result.Value.ByAmount, true)
	// Recompute the value rows explicitly to avoid conflating the two dimensions.
	var redeemedValue, unusedValue string
	for _, entry := range result.Value.ByAmount {
		redeemedValue = addDecimal(redeemedValue, entry.Redeemed)
		unusedValue = addDecimal(unusedValue, entry.Unused)
	}
	result.Value.Redeemed, result.Value.Unused = redeemedValue, unusedValue
	result.Value.Total = addDecimal(redeemedValue, unusedValue)
	result.Quantity.Redeemed = fmt.Sprintf("%d", redeemedCount)
	result.Quantity.Unused = fmt.Sprintf("%d", unusedCount)
	result.Quantity.Total = fmt.Sprintf("%d", redeemedCount+unusedCount)
	result.RedemptionRate = 0
	if redeemedCount+unusedCount > 0 {
		result.RedemptionRate = float64(redeemedCount) / float64(redeemedCount+unusedCount)
	}
	if len(statuses) > 0 {
		result.BatchCount, err = s.store.CountCDKeyBatches(ctx, sqlc.CountCDKeyBatchesParams{
			Statuses: statuses, Amount: amount, BatchID: batchID,
			Search:   opsutil.TextNarg(strings.TrimSpace(params.Search)),
			FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To),
		})
		if err != nil {
			return Summary{}, storeFailed(err, "count CDKEY batches")
		}
		// Batch detail was added after the initial Store contract. Keep it an
		// optional capability so existing test fakes and integrations remain
		// source-compatible while the generated sqlc store supplies it.
		if batchStore, ok := s.store.(interface {
			GetCDKeyBatchSummary(context.Context, sqlc.GetCDKeyBatchSummaryParams) ([]sqlc.GetCDKeyBatchSummaryRow, error)
		}); ok {
			batchRows, batchErr := batchStore.GetCDKeyBatchSummary(ctx, sqlc.GetCDKeyBatchSummaryParams{
				Statuses: statuses, Amount: amount, BatchID: batchID,
				Search:   opsutil.TextNarg(strings.TrimSpace(params.Search)),
				FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To),
			})
			if batchErr != nil {
				return Summary{}, storeFailed(batchErr, "summarize CDKEY batches")
			}
			for _, row := range batchRows {
				key := opsutil.NumericString(row.Amount)
				entry := BatchSummaryAmount{
					BatchCount:              row.BatchCount,
					BatchesWithUnused:       row.BatchesWithUnused,
					FullyRedeemedBatchCount: row.FullyRedeemedBatchCount,
				}
				result.BatchByAmount[key] = entry
				result.BatchesWithUnused += row.BatchesWithUnused
				result.FullyRedeemedBatchCount += row.FullyRedeemedBatchCount
			}
		}
	}
	// Revoked count is an operational side metric; it is intentionally not used in any card denominator.
	if includeRevoked {
		revokedRows, revokedErr := s.store.CountCDKeys(ctx, sqlc.CountCDKeysParams{
			Statuses: []string{statusRevoked}, Amount: amount, BatchID: batchID,
			Search:   opsutil.TextNarg(strings.TrimSpace(params.Search)),
			FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To),
		})
		if revokedErr != nil {
			return Summary{}, storeFailed(revokedErr, "count revoked CDKEYs")
		}
		result.RevokedCount = revokedRows
	}
	return result, nil
}

// ListRedemptions returns the immutable redemption audit list.
func (s *Service) ListRedemptions(ctx context.Context, params ListParams) (RedemptionsResult, error) {
	amount, err := amountArg(params.Amount)
	if err != nil {
		return RedemptionsResult{}, err
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}
	rows, err := s.store.ListCDKeyRedemptionsPage(ctx, sqlc.ListCDKeyRedemptionsPageParams{Search: opsutil.TextNarg(strings.TrimSpace(params.Search)), Amount: amount, FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To), SortField: opsutil.TextNarg(params.Sort), SortDesc: opsutil.BoolNarg(params.Desc), PageLimit: params.Limit, PageOffset: params.Offset})
	if err != nil {
		return RedemptionsResult{}, storeFailed(err, "list CDKEY redemptions")
	}
	total := int64(0)
	if len(rows) > 0 {
		total = rows[0].TotalCount
	} else {
		total, err = s.store.CountCDKeyRedemptions(ctx, sqlc.CountCDKeyRedemptionsParams{Search: opsutil.TextNarg(strings.TrimSpace(params.Search)), Amount: amount, FromTime: opsutil.TsNarg(params.From), ToTime: opsutil.TsNarg(params.To)})
		if err != nil {
			return RedemptionsResult{}, storeFailed(err, "count CDKEY redemptions")
		}
	}
	items := make([]Redemption, 0, len(rows))
	for _, row := range rows {
		items = append(items, Redemption{ID: row.ID, CDKeyID: row.CdkeyID, BatchID: uuidString(row.BatchID), MaskedCode: corecdkey.Mask("UNIO-" + row.CodePrefix + "-0000-0000-" + row.CodeSuffix), UserID: row.UserID, UserDisplayName: row.UserDisplayName, UserEmail: row.UserEmail, Amount: opsutil.NumericString(row.Amount), Currency: row.Currency, LedgerEntryID: row.LedgerEntryID, RedeemedAt: row.RedeemedAt.Time})
	}
	return RedemptionsResult{Items: items, Total: total}, nil
}

func (s *Service) Revoke(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "id must be positive")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return storeFailed(err, "begin CDKEY revoke transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q, queryErr := s.storeForTx(tx)
	if queryErr != nil {
		return queryErr
	}
	row, err := q.GetCDKeyByIDForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("CDKEY not found")
	}
	if err != nil {
		return storeFailed(err, "lock CDKEY")
	}
	if row.Status == statusRedeemed {
		return conflict("redeemed CDKEY cannot be revoked")
	}
	if row.Status == statusUnused {
		if _, err := q.RevokeCDKeyIfUnused(ctx, id); err != nil {
			return storeFailed(err, "revoke CDKEY")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return storeFailed(err, "commit CDKEY revoke")
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "id must be positive")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return storeFailed(err, "begin CDKEY delete transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q, queryErr := s.storeForTx(tx)
	if queryErr != nil {
		return queryErr
	}
	row, err := q.GetCDKeyByIDForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("CDKEY not found")
	}
	if err != nil {
		return storeFailed(err, "lock CDKEY")
	}
	if row.Status == statusRedeemed {
		return conflict("redeemed CDKEY cannot be deleted")
	}
	if _, err := q.DeleteCDKey(ctx, id); err != nil {
		return storeFailed(err, "delete CDKEY")
	}
	if err := tx.Commit(ctx); err != nil {
		return storeFailed(err, "commit CDKEY delete")
	}
	return nil
}

type BulkResult struct {
	Requested int `json:"requested"`
	Affected  int `json:"affected"`
	Skipped   int `json:"skipped"`
}

func (s *Service) BulkRevoke(ctx context.Context, ids []int64) (BulkResult, error) {
	return s.BulkRevokeSelection(ctx, Selection{Scope: "selected", IDs: ids})
}

// BulkRevokeSelection revokes all unused keys in an explicit or filtered selection.
func (s *Service) BulkRevokeSelection(ctx context.Context, selection Selection) (BulkResult, error) {
	ids, err := s.resolveSelection(ctx, selection)
	if err != nil {
		return BulkResult{}, err
	}
	rows, tx, q, err := s.lockBulk(ctx, ids, "revoke")
	if err != nil {
		return BulkResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result := BulkResult{Requested: len(ids)}
	for _, row := range rows {
		switch row.Status {
		case statusUnused:
			if _, err := q.RevokeCDKeyIfUnused(ctx, row.ID); err != nil {
				return BulkResult{}, storeFailed(err, "revoke CDKEYs")
			}
			result.Affected++
		case statusRevoked:
			result.Skipped++
		case statusRedeemed:
			result.Skipped++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkResult{}, storeFailed(err, "commit bulk CDKEY revoke")
	}
	return result, nil
}

func (s *Service) BulkDelete(ctx context.Context, ids []int64) (BulkResult, error) {
	return s.BulkDeleteSelection(ctx, Selection{Scope: "selected", IDs: ids})
}

// BulkDeleteSelection deletes only unused/revoked keys. If any selected key is
// redeemed the entire transaction is rejected, so no partial deletion occurs.
func (s *Service) BulkDeleteSelection(ctx context.Context, selection Selection) (BulkResult, error) {
	ids, err := s.resolveSelection(ctx, selection)
	if err != nil {
		return BulkResult{}, err
	}
	rows, tx, q, err := s.lockBulk(ctx, ids, "delete")
	if err != nil {
		return BulkResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, row := range rows {
		if row.Status == statusRedeemed {
			return BulkResult{}, conflict("bulk delete rejected: selection contains redeemed CDKEY")
		}
	}
	result := BulkResult{Requested: len(ids)}
	for _, row := range rows {
		if _, err := q.DeleteCDKey(ctx, row.ID); err != nil {
			return BulkResult{}, storeFailed(err, "delete CDKEYs")
		}
		result.Affected++
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkResult{}, storeFailed(err, "commit bulk CDKEY delete")
	}
	return result, nil
}

// resolveSelection turns a UI selection into a concrete, de-duplicated ID set.
// Filter resolution happens in SQL so "select all filtered" is not limited to
// the currently loaded page.
func (s *Service) resolveSelection(ctx context.Context, selection Selection) ([]int64, error) {
	scope := strings.TrimSpace(selection.Scope)
	if scope == "" {
		scope = "selected"
	}
	var ids []int64
	switch scope {
	case "selected", "page":
		ids = append(ids, selection.IDs...)
	case "filter":
		amount, err := amountArg(selection.Filter.Amount)
		if err != nil {
			return nil, err
		}
		batchID, err := uuidArg(selection.Filter.BatchID)
		if err != nil {
			return nil, err
		}
		statuses, err := normalizeStatuses(selection.Filter.Statuses)
		if err != nil {
			return nil, err
		}
		ids, err = s.store.ListCDKeyIDs(ctx, sqlc.ListCDKeyIDsParams{
			Statuses: statuses,
			Amount:   amount,
			BatchID:  batchID,
			Search:   opsutil.TextNarg(strings.TrimSpace(selection.Filter.Search)),
			FromTime: opsutil.TsNarg(selection.Filter.From),
			ToTime:   opsutil.TsNarg(selection.Filter.To),
		})
		if err != nil {
			return nil, storeFailed(err, "resolve CDKEY selection")
		}
	default:
		return nil, invalidArgument("scope", "scope must be selected, page, or filter")
	}
	if len(ids) == 0 {
		return nil, invalidArgument("ids", "selection must contain at least one CDKEY")
	}
	excluded := make(map[int64]struct{}, len(selection.ExcludeIDs))
	for _, id := range selection.ExcludeIDs {
		if id <= 0 {
			return nil, invalidArgument("exclude_ids", "exclude_ids must contain positive integers")
		}
		excluded[id] = struct{}{}
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, invalidArgument("ids", "ids must contain positive integers")
		}
		if _, skip := excluded[id]; skip {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, invalidArgument("ids", "selection is empty after exclusions")
	}
	return out, nil
}

func (s *Service) lockBulk(ctx context.Context, ids []int64, operation string) ([]sqlc.Cdkey, pgx.Tx, Store, error) {
	if len(ids) == 0 {
		return nil, nil, nil, invalidArgument("ids", "ids must not be empty")
	}
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, nil, nil, invalidArgument("ids", "ids must contain positive integers")
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, nil, nil, storeFailed(err, "begin bulk CDKEY "+operation+" transaction")
	}
	q, queryErr := s.storeForTx(tx)
	if queryErr != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, nil, queryErr
	}
	rows, err := q.GetCDKeysForUpdateByIDs(ctx, unique)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, nil, storeFailed(err, "lock CDKEYs")
	}
	if len(rows) != len(unique) {
		_ = tx.Rollback(ctx)
		return nil, nil, nil, notFound("one or more CDKEYs not found")
	}
	return rows, tx, q, nil
}

func (s *Service) storeForTx(tx pgx.Tx) (Store, error) {
	if factory, ok := s.store.(TxStoreFactory); ok {
		store := factory.StoreForTx(tx)
		if store != nil {
			return store, nil
		}
	}
	if queries, ok := s.store.(*sqlc.Queries); ok {
		return queries.WithTx(tx), nil
	}
	return nil, storeFailed(errors.New("CDKEY store is not transaction capable"), "begin CDKEY transaction")
}

// Export returns rows for an authenticated CSV response. It validates status
// selection before touching the plaintext column.
func (s *Service) Export(ctx context.Context, params ExportParams) ([]ExportRow, error) {
	statuses, err := normalizeStatuses(params.Statuses)
	if err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, invalidArgument("statuses", "at least one status is required")
	}
	if params.Scope == "selected" || params.Scope == "page" {
		for _, id := range params.IDs {
			if id <= 0 {
				return nil, invalidArgument("ids", "ids must contain positive integers")
			}
		}
	}
	if params.Scope == "" {
		params.Scope = "all"
	}
	var rows []ExportRow
	switch params.Scope {
	case "selected", "page":
		if len(params.IDs) == 0 {
			return nil, invalidArgument("ids", "ids must not be empty for selected/page export")
		}
		got, queryErr := s.store.ExportCDKeysByIDs(ctx, sqlc.ExportCDKeysByIDsParams{Ids: params.IDs, Statuses: statuses, ExcludeIds: []int64{}})
		if queryErr != nil {
			return nil, storeFailed(queryErr, "export CDKEYs")
		}
		rows = make([]ExportRow, 0, len(got))
		for _, row := range got {
			rows = append(rows, exportFromIDsRow(row))
		}
	case "all":
		got, queryErr := s.store.ExportCDKeysByFilter(ctx, sqlc.ExportCDKeysByFilterParams{Statuses: statuses, ExcludeIds: []int64{}})
		if queryErr != nil {
			return nil, storeFailed(queryErr, "export CDKEYs")
		}
		rows = make([]ExportRow, 0, len(got))
		for _, row := range got {
			rows = append(rows, exportFromFilterRow(row))
		}
	default:
		return nil, invalidArgument("scope", "scope must be selected, page, or all")
	}
	return rows, nil
}

func normalizeStatuses(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !corecdkey.IsStatus(value) {
			return nil, invalidArgument("statuses", "statuses contains an unsupported value")
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out, nil
}

func nonRevokedStatuses(values []string) []string {
	out := make([]string, 0, 2)
	for _, value := range values {
		if value != statusRevoked {
			out = append(out, value)
		}
	}
	return out
}

func amountArg(value string) (pgtype.Numeric, error) {
	if strings.TrimSpace(value) == "" {
		return pgtype.Numeric{}, nil
	}
	n, ok := corecdkey.AmountNumeric(value)
	if !ok {
		return pgtype.Numeric{}, invalidArgument("amount", "amount must be one of the supported USD denominations")
	}
	return n, nil
}

func uuidArg(value string) (pgtype.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return pgtype.UUID{}, nil
	}
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return pgtype.UUID{}, invalidArgument("batch_id", "batch_id must be a valid UUID")
	}
	return pgUUID(id), nil
}

func pgUUID(value uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: value, Valid: true} }

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func cdkeyFromListRow(row sqlc.ListCDKeysPageRow) CDKey {
	item := CDKey{
		ID:         row.ID,
		BatchID:    uuidString(row.BatchID),
		MaskedCode: corecdkey.Prefix + "-" + row.CodePrefix + "-****-****-" + row.CodeSuffix,
		CodePrefix: row.CodePrefix,
		CodeSuffix: row.CodeSuffix,
		Amount:     opsutil.NumericString(row.Amount),
		Currency:   row.Currency, Status: row.Status,
		CreatedAt:  row.CreatedAt.Time,
		RedeemedAt: opsutil.TimeValue(row.RedeemedAt),
		RevokedAt:  opsutil.TimeValue(row.RevokedAt),
	}
	item.RedemptionID, item.RedemptionUserID, item.RedemptionLedgerID = opsutil.Int8Value(row.RedemptionID), opsutil.Int8Value(row.RedemptionUserID), opsutil.Int8Value(row.RedemptionLedgerEntryID)
	item.RedemptionUserEmail, item.RedemptionUserDisplayName, item.RedemptionAt = opsutil.TextPtr(row.RedemptionUserEmail), opsutil.TextPtr(row.RedemptionUserDisplayName), opsutil.TimeValue(row.RedemptionRedeemedAt)
	return item
}

func exportFromIDsRow(row sqlc.ExportCDKeysByIDsRow) ExportRow {
	return ExportRow{ID: row.ID, BatchID: uuidString(row.BatchID), CodePlaintext: row.CodePlaintext, Amount: opsutil.NumericString(row.Amount), Currency: row.Currency, Status: row.Status, CreatedAt: row.CreatedAt.Time, RedeemedAt: opsutil.TimeValue(row.RedeemedAt), RevokedAt: opsutil.TimeValue(row.RevokedAt), RedemptionUserID: opsutil.Int8Value(row.RedemptionUserID), RedemptionUserEmail: opsutil.TextPtr(row.RedemptionUserEmail), RedemptionLedgerID: opsutil.Int8Value(row.RedemptionLedgerEntryID), RedemptionAt: opsutil.TimeValue(row.RedemptionRedeemedAt)}
}

func exportFromFilterRow(row sqlc.ExportCDKeysByFilterRow) ExportRow {
	return ExportRow{ID: row.ID, BatchID: uuidString(row.BatchID), CodePlaintext: row.CodePlaintext, Amount: opsutil.NumericString(row.Amount), Currency: row.Currency, Status: row.Status, CreatedAt: row.CreatedAt.Time, RedeemedAt: opsutil.TimeValue(row.RedeemedAt), RevokedAt: opsutil.TimeValue(row.RevokedAt), RedemptionUserID: opsutil.Int8Value(row.RedemptionUserID), RedemptionUserEmail: opsutil.TextPtr(row.RedemptionUserEmail), RedemptionLedgerID: opsutil.Int8Value(row.RedemptionLedgerEntryID), RedemptionAt: opsutil.TimeValue(row.RedemptionRedeemedAt)}
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func conflict(message string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(message))
}

func storeFailed(err error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(message))
}

// addDecimal uses exact rational arithmetic through the existing decimal helper.
func addDecimal(left, right string) string {
	if left == "" {
		return right
	}
	// opsutil has subtraction but no addition; use numeric parsing without floats.
	// Values here are fixed integer denominations, so integer addition is sufficient.
	var l, r int64
	_, _ = fmt.Sscan(left, &l)
	_, _ = fmt.Sscan(right, &r)
	return fmt.Sprintf("%d", l+r)
}

func multiplyAmount(amount string, quantity int) string {
	var value int64
	if _, err := fmt.Sscan(amount, &value); err != nil {
		return "0"
	}
	return fmt.Sprintf("%d", value*int64(quantity))
}

func sumSummaryValues(values map[string]SummaryAmount, _ bool) string {
	var total string
	for _, value := range values {
		total = addDecimal(total, value.Redeemed)
		total = addDecimal(total, value.Unused)
	}
	return total
}

// compile-time assertion for the concrete generated store.
var _ Store = (*sqlc.Queries)(nil)
