package capability

import (
	"context"
	"math/big"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestSupportLevelValidators(t *testing.T) {
	modelValid := []SupportLevel{SupportLevelFull, SupportLevelLimited, SupportLevelUnsupported}
	for _, level := range modelValid {
		if !IsValidSupportLevel(level) {
			t.Fatalf("expected model support level %q valid", level)
		}
	}
	if IsValidSupportLevel("partial") {
		t.Fatal("expected unknown support level to be invalid")
	}
}

func TestSourceValidators(t *testing.T) {
	if !IsValidSyncJobSource(SourceModelsDev) || !IsValidSyncJobSource(SourceManual) {
		t.Fatal("expected sync job to allow models_dev/manual")
	}
	if IsValidSyncJobSource(Source("adapter_seed")) {
		t.Fatal("expected sync job to reject adapter_seed")
	}
	if IsValidSyncJobSource(Source("import")) {
		t.Fatal("expected sync job to reject unknown source")
	}
}

func TestNumericDecimalString(t *testing.T) {
	cases := []struct {
		name  string
		value pgtype.Numeric
		want  *string
	}{
		{
			name:  "null",
			value: pgtype.Numeric{Valid: false},
			want:  nil,
		},
		{
			name:  "nan",
			value: pgtype.Numeric{NaN: true, Valid: true},
			want:  nil,
		},
		{
			name:  "fraction",
			value: pgtype.Numeric{Int: big.NewInt(25), Exp: -1, Valid: true},
			want:  strPtr("2.5"),
		},
		{
			name:  "small fraction",
			value: pgtype.Numeric{Int: big.NewInt(5), Exp: -10, Valid: true},
			want:  strPtr("0.0000000005"),
		},
		{
			name:  "scaled integer",
			value: pgtype.Numeric{Int: big.NewInt(25), Exp: 8, Valid: true},
			want:  strPtr("2500000000"),
		},
		{
			name:  "plain integer",
			value: pgtype.Numeric{Int: big.NewInt(42), Exp: 0, Valid: true},
			want:  strPtr("42"),
		},
		{
			name:  "negative",
			value: pgtype.Numeric{Int: big.NewInt(-15), Exp: -1, Valid: true},
			want:  strPtr("-1.5"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := numericDecimalString(tc.value)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("expected nil, got %q", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("expected %q, got nil", *tc.want)
			case tc.want != nil && got != nil && *got != *tc.want:
				t.Fatalf("expected %q, got %q", *tc.want, *got)
			}
		})
	}
}

func TestStoreRejectsInvalidWritesBeforeDB(t *testing.T) {
	// queries 传 nil：合法性校验必须在触达 DB 之前短路，否则会 panic。
	// 能力 key 合法性改由 capability_keys 字典外键在 DB 层兜底（DEC-024），不再于此预检。
	store := NewStore(nil)
	ctx := context.Background()

	_, err := store.UpsertModelCapability(ctx, UpsertModelCapabilityParams{
		ModelID:      1,
		Key:          "text.input",
		SupportLevel: "partial",
	})
	assertFailureCode(t, err, failure.CodeCapabilityInvalidSupportLevel)

	_, err = store.CreateSyncJob(ctx, Source("adapter_seed"))
	assertFailureCode(t, err, failure.CodeCapabilityInvalidSource)
}

func assertFailureCode(t *testing.T, err error, want failure.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected failure code %q, got nil error", want)
	}
	if got := failure.CodeOf(err); got != want {
		t.Fatalf("expected failure code %q, got %q (%v)", want, got, err)
	}
}

func strPtr(s string) *string {
	return &s
}
