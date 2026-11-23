package config

import (
	"strings"
	"testing"
)

func lookup(env map[string]string) func(string) string {
	return func(name string) string { return env[name] }
}

func TestEnv(t *testing.T) {
	env, err := FromEnviron(lookup(map[string]string{"BOT_TOKEN": "SECRET", "OWNER_ID": " 42 ", "INVITE_DAYS": "3",
		"TZ": "Asia/Yekaterinburg"}))
	if err != nil {
		t.Fatal(err)
	}
	if env.OwnerID != 42 || env.InviteDays != 3 || env.TZ != "Asia/Yekaterinburg" ||
		env.APIURL != "https://api.telegram.org" {
		t.Fatalf("got %+v", env)
	}
	if strings.Contains(env.String(), "SECRET") {
		t.Fatal("the token is in the string")
	}
}

func TestEnvErrors(t *testing.T) {
	for _, c := range []struct {
		env   map[string]string
		error string
	}{
		{map[string]string{}, "BOT_TOKEN, OWNER_ID"},
		{map[string]string{"BOT_TOKEN": "t", "OWNER_ID": "me"}, "OWNER_ID must be an integer"},
		{map[string]string{"BOT_TOKEN": "t", "OWNER_ID": "1", "INVITE_DAYS": "0"}, "INVITE_DAYS"},
		{map[string]string{"BOT_TOKEN": "t", "OWNER_ID": "1", "TZ": "Mars/Olympus"}, "TZ is not a time zone"},
		{map[string]string{"BOT_TOKEN": "t", "OWNER_ID": "1", "BOT_API_URL": "ftp://x"}, "BOT_API_URL"},
	} {
		_, err := FromEnviron(lookup(c.env))
		if err == nil || !strings.Contains(err.Error(), c.error) {
			t.Errorf("%v: got %v, want %q", c.env, err, c.error)
		}
	}
}
