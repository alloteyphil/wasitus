package lab

import (
	"testing"
	"time"
)

func TestEnvDuration(t *testing.T) {
	tests := []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{"", 3 * time.Second, false}, // unset: fallback
		{"250ms", 250 * time.Millisecond, false},
		{"3", 0, true}, // no unit
		{"-1s", 0, true},
		{"0s", 0, true},
	}
	for _, tt := range tests {
		t.Setenv("LAB_TEST_TIMEOUT", tt.value)
		got, err := EnvDuration("LAB_TEST_TIMEOUT", 3*time.Second)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("EnvDuration(%q) = %v, %v; want %v, error %v", tt.value, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestEnvInt(t *testing.T) {
	tests := []struct {
		value   string
		want    int
		wantErr bool
	}{
		{"", 0, false},
		{"5", 5, false},
		{"0", 0, false},
		{"-2", 0, true},
		{"every fifth", 0, true},
	}
	for _, tt := range tests {
		t.Setenv("LAB_TEST_N", tt.value)
		got, err := EnvInt("LAB_TEST_N", 0)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("EnvInt(%q) = %v, %v; want %v, error %v", tt.value, got, err, tt.want, tt.wantErr)
		}
	}
}
