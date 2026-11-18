package main

import "testing"

func TestRestartRequiredUsesRunningRole(t *testing.T) {
	for _, tc := range []struct {
		boot, saved string
		want        bool
	}{
		{"camera", "master", true},
		{"master", "camera", true},
		{"camera", "presenter", false},
		{"master", "master", false}, // Saving again or reverting before restart.
		{"camera", "camera", false},
	} {
		if got := restartRequired(tc.boot, tc.saved); got != tc.want {
			t.Errorf("restartRequired(%q, %q) = %v, want %v", tc.boot, tc.saved, got, tc.want)
		}
	}
}
