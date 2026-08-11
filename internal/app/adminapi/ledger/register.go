package ledger

import "github.com/go-chi/chi/v5"

// Deps 是账本模块的路由依赖。
type Deps struct {
	Service LedgerQueryService
}

// Register 注册账本模块路由。
func Register(r chi.Router, d Deps) {
	// M6 只读查询台：账本流水、计费异常。全部只读。
	if d.Service != nil {
		lh := &ledgerHandler{service: d.Service}
		r.Get("/ledger/entries", lh.listEntries)
		r.Get("/ledger/billing-exceptions", lh.listBillingExceptions)
	}
}
