package gothon

import (
	"slices"
	"testing"
	"time"
)

func TestSettingsEnv(t *testing.T) {
	f, no, zero := 0.5, false, 0
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got := SettingsEnv(
		"KEY", "secret",
		"EMPTY", "",
		"RATE", &f,
		"UNSET", (*float64)(nil),
		"DEBUG", &no,
		"RETRIES", &zero,
		"TAGS", []string{"a", "b"},
		"SINCE", when,
		"ANY", nil,
	)
	want := []string{
		"KEY=secret",
		"RATE=0.5",
		"DEBUG=false",
		"RETRIES=0",
		`TAGS=["a","b"]`,
		"SINCE=2026-01-02T03:04:05Z",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
