package capability

import (
	"context"
	"math/big"
	"testing"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIsRegisteredKey(t *testing.T) {
	registered := []Key{
		KeyTextInput,
		KeyToolsFunction,
		KeyToolsBuiltinWebSearch,
		KeyReasoningEffort,
		KeyResponsesEncryptedContent,
	}
	for _, key := range registered {
		if !IsRegisteredKey(key) {
			t.Fatalf("expected key %q to be registered", key)
		}
	}

	unknown := []Key{"", "text", "tools.unknown", "reasoning.effort.extra", "TEXT.INPUT"}
	for _, key := range unknown {
		if IsRegisteredKey(key) {
			t.Fatalf("expected key %q to be unregistered", key)
		}
	}
}

func TestRegisteredKeysMatchesRegistry(t *testing.T) {
	keys := RegisteredKeys()

	if len(keys) != len(registeredKeys) {
		t.Fatalf("expected %d registered keys, got %d", len(registeredKeys), len(keys))
	}

	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("expected registered keys sorted ascending, got %q before %q", keys[i-1], keys[i])
		}
	}

	for _, key := range keys {
		if !IsRegisteredKey(key) {
			t.Fatalf("RegisteredKeys returned unregistered key %q", key)
		}
	}
}

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

	if IsValidChannelOverrideLevel(SupportLevelFull) {
		t.Fatal("expected channel override to reject full")
	}
	if !IsValidChannelOverrideLevel(SupportLevelLimited) || !IsValidChannelOverrideLevel(SupportLevelUnsupported) {
		t.Fatal("expected channel override to allow limited/unsupported")
	}
}

func TestSourceValidators(t *testing.T) {
	for _, source := range []Source{SourceModelsDev, SourceManual, SourceAdapterSeed} {
		if !IsValidCapabilitySource(source) {
			t.Fatalf("expected capability source %q valid", source)
		}
	}
	if IsValidCapabilitySource("import") {
		t.Fatal("expected unknown capability source to be invalid")
	}

	if !IsValidSyncJobSource(SourceModelsDev) || !IsValidSyncJobSource(SourceManual) {
		t.Fatal("expected sync job to allow models_dev/manual")
	}
	if IsValidSyncJobSource(SourceAdapterSeed) {
		t.Fatal("expected sync job to reject adapter_seed")
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
	store := NewStore(nil)
	ctx := context.Background()

	_, err := store.UpsertModelCapability(ctx, UpsertModelCapabilityParams{
		ModelID:      1,
		Key:          "tools.unknown",
		SupportLevel: SupportLevelFull,
		Source:       SourceManual,
	})
	assertFailureCode(t, err, failure.CodeCapabilityInvalidKey)

	_, err = store.UpsertModelCapability(ctx, UpsertModelCapabilityParams{
		ModelID:      1,
		Key:          KeyTextInput,
		SupportLevel: "partial",
		Source:       SourceManual,
	})
	assertFailureCode(t, err, failure.CodeCapabilityInvalidSupportLevel)

	_, err = store.UpsertModelCapability(ctx, UpsertModelCapabilityParams{
		ModelID:      1,
		Key:          KeyTextInput,
		SupportLevel: SupportLevelFull,
		Source:       "import",
	})
	assertFailureCode(t, err, failure.CodeCapabilityInvalidSource)

	_, err = store.UpsertChannelOverride(ctx, UpsertChannelOverrideParams{
		ChannelID:    1,
		Key:          KeyTextInput,
		SupportLevel: SupportLevelFull,
	})
	assertFailureCode(t, err, failure.CodeCapabilityInvalidSupportLevel)

	_, err = store.CreateSyncJob(ctx, SourceAdapterSeed)
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
