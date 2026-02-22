package irc

import (
	"strings"
	"testing"
)

func TestValidateChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		channel  string
		caller   string
		wantErr  bool
		errMatch string
	}{
		{
			name:    "valid channel",
			channel: "#general",
			caller:  "test",
			wantErr: false,
		},
		{
			name:     "empty channel",
			channel:  "",
			caller:   "join",
			wantErr:  true,
			errMatch: "channel name is required",
		},
		{
			name:     "missing hash prefix",
			channel:  "general",
			caller:   "part",
			wantErr:  true,
			errMatch: "must start with '#'",
		},
		{
			name:    "channel with multiple hashes",
			channel: "##test",
			caller:  "test",
			wantErr: false,
		},
		{
			name:     "caller included in error",
			channel:  "",
			caller:   "kick",
			wantErr:  true,
			errMatch: "kick:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateChannel(tt.channel, tt.caller)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMatch != "" && !strings.Contains(err.Error(), tt.errMatch) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errMatch)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
