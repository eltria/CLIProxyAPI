package auth

import (
	"testing"
	"time"
)

func TestCapQuotaRetryAfter(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"under cap stays intact", 5 * time.Minute, 5 * time.Minute},
		{"exactly at cap stays intact", quotaRetryAfterCap, quotaRetryAfterCap},
		{"over cap gets clamped", 100 * time.Hour, quotaRetryAfterCap},
		{"zero passes through", 0, 0},
		{"negative passes through", -5 * time.Second, -5 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := capQuotaRetryAfter(tc.in); got != tc.want {
				t.Fatalf("capQuotaRetryAfter(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
