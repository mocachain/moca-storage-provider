package blocksyncer

import "testing"

func TestShouldSwitchMasterDB(t *testing.T) {
	tests := []struct {
		name       string
		master     int64
		backup     int64
		wantSwitch bool
	}{
		{name: "backup ahead", master: 100, backup: 101, wantSwitch: false},
		{name: "same height", master: 100, backup: 100, wantSwitch: true},
		{name: "master within gap", master: 199, backup: 100, wantSwitch: true},
		{name: "master at gap", master: 200, backup: 100, wantSwitch: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSwitchMasterDB(tt.master, tt.backup); got != tt.wantSwitch {
				t.Fatalf("shouldSwitchMasterDB(%d, %d) = %v, want %v", tt.master, tt.backup, got, tt.wantSwitch)
			}
		})
	}
}
