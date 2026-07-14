package ledger

import "github.com/go-chi/chi/v5"

// Deps 是账本模块的路由依赖（账本流水/计费异常 + bill-on-cancel 渠道成本敞口）。
type Deps struct {
	Service             LedgerQueryService
	CostExposureService CostExposureQueryService
}

// Register 注册账本模块路由。静态 /channels/cost-exposures/summary 在 /channels/{id} 之前注册。
func Register(r chi.Router, d Deps) {
	// bill-on-cancel 渠道成本敞口：静态 summary 在 /channels/{id} 之前注册。
	if d.CostExposureService != nil {
		ceh := &costExposuresHandler{service: d.CostExposureService}
		r.Get("/channels/cost-exposures/summary", ceh.summary)
		r.Get("/channels/{id}/cost-exposures", ceh.list)
	}

	// M6 只读查询台：账本流水、计费异常。全部只读。
	if d.Service != nil {
		lh := &ledgerHandler{service: d.Service}
		r.Get("/ledger/entries", lh.listEntries)
		r.Get("/ledger/billing-exceptions", lh.listBillingExceptions)
	}
}
