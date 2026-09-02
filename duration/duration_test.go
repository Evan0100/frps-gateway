package duration

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"45s", 45 * time.Second, false},
		{"30m", 30 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"3d", 72 * time.Hour, false},
		{"1d12h", 36 * time.Hour, false},
		{"2h30m", 150 * time.Minute, false},
		{"1m30s", 90 * time.Second, false},
		{"0.5d", 12 * time.Hour, false},
		{" 2h ", 2 * time.Hour, false},
		{"2H", 2 * time.Hour, false},
		{"", 0, true},
		{"abc", 0, true},
		{"100", 0, true},
		{"1x", 0, true},
		{"-5m", 0, true},
		{"1 d", 0, true},
		{"d", 0, true},
		{"1.5.5h", 0, true},
	}
	for _, tc := range cases {
		got, err := Parse(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Parse(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{2 * time.Hour, "2h"},
		{3*24*time.Hour + 2*time.Hour, "3d2h"},
		{5*time.Hour + 3*time.Minute, "5h3m"},
		{2*time.Minute + 30*time.Second, "2m30s"},
		{36 * time.Hour, "1d12h"},
		{90 * time.Minute, "1h30m"},
		{0, "0s"},
		{-time.Second, "0s"},
		{time.Second, "1s"},
	}
	for _, tc := range cases {
		if got := Format(tc.in); got != tc.want {
			t.Errorf("Format(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
