package bot

import (
	"testing"
	"time"
)

func TestSnowflakeTime(t *testing.T) {
	cases := []struct {
		name      string
		snowflake string
		wantOK    bool
		wantYear  int
	}{
		{
			name:      "real Discord snowflake",
			snowflake: "847954148335046696",
			wantOK:    true,
			wantYear:  2021,
		},
		{
			name:      "early snowflake",
			snowflake: "175928847299117063",
			wantOK:    true,
			wantYear:  2016,
		},
		{
			name:      "non-numeric input",
			snowflake: "abc",
			wantOK:    false,
		},
		{
			name:      "empty input",
			snowflake: "",
			wantOK:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SnowflakeTime(tc.snowflake)
			if ok != tc.wantOK {
				t.Fatalf("ok mismatch: want %v got %v", tc.wantOK, ok)
			}
			if !ok {
				return
			}
			if got.UTC().Year() != tc.wantYear {
				t.Errorf("got year %d; want %d",
					got.UTC().Year(), tc.wantYear)
			}
		})
	}
}

func TestParseActorFromReason(t *testing.T) {
	cases := []struct {
		name     string
		reason   string
		wantOK   bool
		wantID   string
		wantName string
	}{
		{
			name:   "mention with nick form",
			reason: "Banned <@!123456789012345678>",
			wantOK: true, wantID: "123456789012345678",
		},
		{
			name:   "mention without nick form",
			reason: "Banned <@123456789012345678>",
			wantOK: true, wantID: "123456789012345678",
		},
		{
			name:   "raw snowflake",
			reason: "Case 6 - timeout 123456789012345678 by system",
			wantOK: true, wantID: "123456789012345678",
		},
		{
			name:   "by-name form",
			reason: "Banned by pycharm_enjoyer for 10 seconds",
			wantOK: true, wantName: "pycharm_enjoyer",
		},
		{
			name:   "by-@-name form",
			reason: "Kicked by @bob because spam",
			wantOK: true, wantName: "bob",
		},
		{
			name:   "name with pipe separator",
			reason: "Case 7 | bob | spam",
			wantOK: true, wantName: "bob",
		},
		{
			name:   "snowflake preferred over name when both present",
			reason: "<@123456789012345678> aka bob did the thing",
			wantOK: true, wantID: "123456789012345678",
		},
		{
			name:   "garbage yields no extraction",
			reason: "!@#$%^&*()",
			wantOK: false,
		},
		{
			name:   "empty yields no extraction",
			reason: "",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseActorFromReason(tc.reason)
			if ok != tc.wantOK {
				t.Fatalf("ok mismatch: want %v got %v", tc.wantOK, ok)
			}
			if !ok {
				return
			}
			if got.ID != tc.wantID {
				t.Errorf("ID mismatch: want %q got %q", tc.wantID, got.ID)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name mismatch: want %q got %q", tc.wantName, got.Name)
			}
		})
	}
}

func TestAbsDuration(t *testing.T) {
	if absDuration(-5*time.Second) != 5*time.Second {
		t.Errorf("negative input not handled")
	}
	if absDuration(3*time.Second) != 3*time.Second {
		t.Errorf("positive input changed")
	}
	if absDuration(0) != 0 {
		t.Errorf("zero changed")
	}
}

func absTime(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
