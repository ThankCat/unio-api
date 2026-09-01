package auth

import (
	"strings"
	"testing"
)

func TestValidateDisplayName(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "ascii", value: "User2026", valid: true},
		{name: "chinese", value: "陈一二三", valid: true},
		{name: "mixed", value: "用户User123", valid: true},
		{name: "one character", value: "陈", valid: true},
		{name: "thirty two characters", value: strings.Repeat("名", 32), valid: true},
		{name: "empty", value: ""},
		{name: "thirty three characters", value: strings.Repeat("A", 33)},
		{name: "leading space", value: " User"},
		{name: "trailing space", value: "User "},
		{name: "underscore", value: "user_name"},
		{name: "hyphen", value: "user-name"},
		{name: "punctuation", value: "用户。"},
		{name: "emoji", value: "用户😀"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDisplayName(tt.value)
			if tt.valid {
				if err != nil {
					t.Fatalf("ValidateDisplayName(%q): %v", tt.value, err)
				}
				return
			}
			if err == nil || err.Code != CodeInvalidDisplayName || err.Param != "display_name" || err.Status != 422 {
				t.Fatalf("ValidateDisplayName(%q) = %+v", tt.value, err)
			}
		})
	}
}
