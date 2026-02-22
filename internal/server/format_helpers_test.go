package server

import (
	"database/sql"
	"testing"
	"time"

	"murmur/internal/config"
)

func TestIsNickAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		nick  string
		users []string
		want  bool
	}{
		{
			name:  "exact match",
			nick:  "alice",
			users: []string{"alice", "bob"},
			want:  true,
		},
		{
			name:  "case insensitive match",
			nick:  "Alice",
			users: []string{"alice", "bob"},
			want:  true,
		},
		{
			name:  "no match",
			nick:  "charlie",
			users: []string{"alice", "bob"},
			want:  false,
		},
		{
			name:  "empty list",
			nick:  "alice",
			users: []string{},
			want:  false,
		},
		{
			name:  "nil list",
			nick:  "alice",
			users: nil,
			want:  false,
		},
		{
			name:  "empty nick",
			nick:  "",
			users: []string{"alice"},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := IsNickAllowed(tt.nick, tt.users)
			if got != tt.want {
				t.Errorf("IsNickAllowed(%q, %v) = %v, want %v", tt.nick, tt.users, got, tt.want)
			}
		})
	}
}

func TestFormatList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []string
		want  string
	}{
		{
			name:  "empty list",
			items: []string{},
			want:  "(all)",
		},
		{
			name:  "nil list",
			items: nil,
			want:  "(all)",
		},
		{
			name:  "single item",
			items: []string{"shell"},
			want:  "shell",
		},
		{
			name:  "multiple items",
			items: []string{"shell", "mail_read", "dns_check"},
			want:  "shell, mail_read, dns_check",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatList(tt.items)
			if got != tt.want {
				t.Errorf("FormatList(%v) = %q, want %q", tt.items, got, tt.want)
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    string
		want []string
	}{
		{
			name: "empty string",
			s:    "",
			want: nil,
		},
		{
			name: "single value",
			s:    "shell",
			want: []string{"shell"},
		},
		{
			name: "comma separated",
			s:    "shell,mail_read,dns_check",
			want: []string{"shell", "mail_read", "dns_check"},
		},
		{
			name: "comma separated with spaces",
			s:    "shell, mail_read , dns_check",
			want: []string{"shell", "mail_read", "dns_check"},
		},
		{
			name: "trailing comma",
			s:    "shell,mail_read,",
			want: []string{"shell", "mail_read"},
		},
		{
			name: "only commas",
			s:    ",,,",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SplitCSV(tt.s)
			if len(got) != len(tt.want) {
				t.Fatalf("SplitCSV(%q) = %v (len %d), want %v (len %d)", tt.s, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SplitCSV(%q)[%d] = %q, want %q", tt.s, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseCSV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   []string
	}{
		{
			name:   "nil input",
			values: nil,
			want:   nil,
		},
		{
			name:   "single plain value",
			values: []string{"shell"},
			want:   []string{"shell"},
		},
		{
			name:   "space separated values",
			values: []string{"shell", "mail_read"},
			want:   []string{"shell", "mail_read"},
		},
		{
			name:   "comma in single element",
			values: []string{"shell,mail_read"},
			want:   []string{"shell", "mail_read"},
		},
		{
			name:   "mixed comma and space",
			values: []string{"shell,mail_read", "dns_check"},
			want:   []string{"shell", "mail_read", "dns_check"},
		},
		{
			name:   "empty values filtered",
			values: []string{"", "shell", ""},
			want:   []string{"shell"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseCSV(tt.values)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseCSV(%v) = %v (len %d), want %v (len %d)", tt.values, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ParseCSV(%v)[%d] = %q, want %q", tt.values, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFormatTaskList(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 2, 22, 15, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		tasks []ScheduledTask
		want  string
	}{
		{
			name:  "empty list",
			tasks: nil,
			want:  "",
		},
		{
			name: "single cron task",
			tasks: []ScheduledTask{
				{
					ID:       1,
					Name:     "check weather",
					Schedule: "0 9 * * *",
					Channel:  "#general",
					Enabled:  true,
					NextRun:  sql.NullTime{Time: fixedTime, Valid: true},
					Type:     TaskTypeCron,
				},
			},
			want: `  #1 [cron] "check weather" [0 9 * * *] #general — next: 2026-02-22 15:00 UTC — enabled`,
		},
		{
			name: "one-shot task with creator",
			tasks: []ScheduledTask{
				{
					ID:        2,
					Name:      "remind me",
					Channel:   "#general",
					Enabled:   true,
					NextRun:   sql.NullTime{Time: fixedTime, Valid: true},
					Type:      TaskTypeOnce,
					RunAt:     sql.NullTime{Time: fixedTime, Valid: true},
					CreatedBy: "alice",
				},
			},
			want: `  #2 [once] "remind me" [at 2026-02-22 15:00 UTC] #general — next: 2026-02-22 15:00 UTC — enabled, by: alice`,
		},
		{
			name: "disabled task no next run",
			tasks: []ScheduledTask{
				{
					ID:      3,
					Name:    "old task",
					Channel: "#ops",
					Enabled: false,
					Type:    TaskTypeCron,
				},
			},
			want: `  #3 [cron] "old task" [] #ops — next: N/A — disabled`,
		},
		{
			name: "task with provider",
			tasks: []ScheduledTask{
				{
					ID:        4,
					Name:      "model-specific task",
					Schedule:  "0 12 * * *",
					Channel:   "#ml",
					Enabled:   true,
					NextRun:   sql.NullTime{Time: fixedTime, Valid: true},
					Type:      TaskTypeCron,
					CreatedBy: "alice",
					Provider:  "openrouter",
				},
			},
			want: `  #4 [cron] "model-specific task" [0 12 * * *] #ml — next: 2026-02-22 15:00 UTC — enabled, by: alice, provider: openrouter`,
		},
		{
			name: "multiple tasks",
			tasks: []ScheduledTask{
				{
					ID:       1,
					Name:     "task a",
					Schedule: "*/5 * * * *",
					Channel:  "#a",
					Enabled:  true,
					NextRun:  sql.NullTime{Time: fixedTime, Valid: true},
					Type:     TaskTypeCron,
				},
				{
					ID:        2,
					Name:      "task b",
					Channel:   "#b",
					Enabled:   true,
					NextRun:   sql.NullTime{Time: fixedTime, Valid: true},
					Type:      TaskTypeOnce,
					RunAt:     sql.NullTime{Time: fixedTime, Valid: true},
					CreatedBy: "bob",
				},
			},
			want: "  #1 [cron] \"task a\" [*/5 * * * *] #a — next: 2026-02-22 15:00 UTC — enabled\n  #2 [once] \"task b\" [at 2026-02-22 15:00 UTC] #b — next: 2026-02-22 15:00 UTC — enabled, by: bob",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatTaskList(tt.tasks)
			if got != tt.want {
				t.Errorf("FormatTaskList() =\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestFormatUserList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		users map[string]config.UserPermissions
		want  string
	}{
		{
			name:  "empty map",
			users: map[string]config.UserPermissions{},
			want:  "",
		},
		{
			name: "single user with role",
			users: map[string]config.UserPermissions{
				"alice": {Role: "admin"},
			},
			want: "  alice [admin]",
		},
		{
			name: "user with empty role defaults to user",
			users: map[string]config.UserPermissions{
				"bob": {},
			},
			want: "  bob [user]",
		},
		{
			name: "multiple users sorted",
			users: map[string]config.UserPermissions{
				"charlie": {Role: "user"},
				"alice":   {Role: "admin"},
				"bob":     {},
			},
			want: "  alice [admin]\n  bob [user]\n  charlie [user]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatUserList(tt.users)
			if got != tt.want {
				t.Errorf("FormatUserList() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestFormatUserPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		user   config.UserPermissions
		want   string
	}{
		{
			name:   "minimal user",
			target: "alice",
			user:   config.UserPermissions{},
			want:   "User: alice\n  Role: user\n  Tools: (all)\n  Autonomy: (default)\n  Models: (all)",
		},
		{
			name:   "full user",
			target: "bob",
			user: config.UserPermissions{
				Role:               "admin",
				Tools:              []string{"shell", "mail_read"},
				DenyTools:          []string{"code_exec"},
				Autonomy:           "approve",
				AllowedModels:      []string{"gpt-4"},
				DenyModels:         []string{"gpt-3"},
				MaxMessagesPerHour: 100,
				APIKey:             "secret",
			},
			want: "User: bob\n  Role: admin\n  Tools: shell, mail_read\n  Deny Tools: code_exec\n  Autonomy: approve\n  Models: gpt-4\n  Deny Models: gpt-3\n  Rate Limit: 100/hr\n  API Key: (set)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatUserPermissions(tt.target, tt.user)
			if got != tt.want {
				t.Errorf("FormatUserPermissions() =\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestFormatChannelList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		channels map[string]config.ChannelPermissions
		want     string
	}{
		{
			name:     "empty map",
			channels: map[string]config.ChannelPermissions{},
			want:     "",
		},
		{
			name: "single channel",
			channels: map[string]config.ChannelPermissions{
				"#general": {Autonomy: "auto"},
			},
			want: "  #general [autonomy: auto]",
		},
		{
			name: "channel with default autonomy",
			channels: map[string]config.ChannelPermissions{
				"#ops": {},
			},
			want: "  #ops [autonomy: (default)]",
		},
		{
			name: "multiple channels sorted",
			channels: map[string]config.ChannelPermissions{
				"#ops":     {Autonomy: "report"},
				"#general": {Autonomy: "auto"},
			},
			want: "  #general [autonomy: auto]\n  #ops [autonomy: report]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatChannelList(tt.channels)
			if got != tt.want {
				t.Errorf("FormatChannelList() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestFormatChannelPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		ch     config.ChannelPermissions
		want   string
	}{
		{
			name:   "minimal channel",
			target: "#general",
			ch:     config.ChannelPermissions{},
			want:   "Channel: #general\n  Tools: (all)\n  Autonomy: (default)\n  Models: (all)",
		},
		{
			name:   "full channel",
			target: "#ops",
			ch: config.ChannelPermissions{
				Tools:         []string{"shell"},
				DenyTools:     []string{"code_exec"},
				Autonomy:      "report",
				AllowedModels: []string{"gpt-4"},
			},
			want: "Channel: #ops\n  Tools: shell\n  Deny Tools: code_exec\n  Autonomy: report\n  Models: gpt-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatChannelPermissions(tt.target, tt.ch)
			if got != tt.want {
				t.Errorf("FormatChannelPermissions() =\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
