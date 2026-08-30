package ledger

// ReservationStatus 表示余额预授权记录状态。
type ReservationStatus string

// BillingExceptionEventType 表示账务异常事实的业务类型。
// write_off 代表真实费用已知但超过冻结金额；risk_exposure 代表真实费用未知但平台可能已有成本风险。
type BillingExceptionEventType string

const (
	ReservationStatusAuthorized ReservationStatus = "authorized"
	ReservationStatusCaptured   ReservationStatus = "captured"
	ReservationStatusReleased   ReservationStatus = "released"
)

// EntryType 表示账本流水类型。
type EntryType string

const (
	EntryTypeCredit           EntryType = "credit"
	EntryTypeDebit            EntryType = "debit"
	EntryTypeRefund           EntryType = "refund"
	EntryTypeAdjustmentCredit EntryType = "adjustment_credit"
	EntryTypeAdjustmentDebit  EntryType = "adjustment_debit"
	// EntryTypeCDKeyCredit 表示通过 CDKEY 兑换产生的用户充值。
	EntryTypeCDKeyCredit EntryType = "cdkey_credit"
)

const (
	// BillingExceptionEventTypeWriteOff 表示平台核销已知差额。
	BillingExceptionEventTypeWriteOff BillingExceptionEventType = "write_off"
	// BillingExceptionEventTypeRiskExposure 表示平台记录未知成本风险敞口。
	BillingExceptionEventTypeRiskExposure BillingExceptionEventType = "risk_exposure"
)
