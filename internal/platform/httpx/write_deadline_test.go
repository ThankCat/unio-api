package httpx

import (
	"testing"
	"time"
)

func TestWriteJSONSetsResponseWriteDeadline(t *testing.T) {
	writer := &deadlineWriter{}
	if err := WriteJSON(writer, 200, map[string]bool{"ok": true}); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if len(writer.deadlines) != 1 || writer.deadlines[0].IsZero() {
		t.Fatalf("JSON write deadlines = %v, want one non-zero deadline", writer.deadlines)
	}
}

func TestClearResponseWriteDeadline(t *testing.T) {
	writer := &deadlineWriter{}
	if err := ClearResponseWriteDeadline(writer); err != nil {
		t.Fatalf("clear response write deadline: %v", err)
	}
	if len(writer.deadlines) != 1 || !writer.deadlines[0].Equal(time.Time{}) {
		t.Fatalf("cleared deadlines = %v, want zero time", writer.deadlines)
	}
}
