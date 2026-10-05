package accounts

// Offline tests for the list_accounts port: a fake AccountLister stands in for
// the connected Telegram clients, so no network and no real session is needed.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/itokun99/telegram-mcp-go/internal/mcpserver"
)

type fakeLister struct {
	labels   []string
	profiles map[string]*mcpserver.SelfProfile
	errs     map[string]error
	calls    []string
}

func (f *fakeLister) Accounts() []string { return f.labels }

func (f *fakeLister) SelfProfile(_ context.Context, label string) (*mcpserver.SelfProfile, error) {
	f.calls = append(f.calls, label)
	if err, ok := f.errs[label]; ok {
		return nil, err
	}
	return f.profiles[label], nil
}

func TestListAccountsToolIsRegistered(t *testing.T) {
	names := mcpserver.RegisteredToolNames()
	for _, want := range []string{"list_accounts"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("tool %q not registered; registry holds %v", want, names)
		}
	}
}

func TestRunListAccountsRendersOneLinePerAccount(t *testing.T) {
	lister := &fakeLister{
		labels: []string{"default", "work"},
		profiles: map[string]*mcpserver.SelfProfile{
			"default": {FirstName: "Ada", LastName: "Lovelace", Phone: "15551234567", StatusType: "UserStatusOnline"},
			"work":    {FirstName: "Grace", Phone: "15559999999", StatusType: "UserStatusLastWeek"},
		},
	}

	got, err := runListAccounts(mcpserver.WithAccountLister(context.Background(), lister))
	if err != nil {
		t.Fatalf("runListAccounts returned error: %v", err)
	}

	want := "default: Ada Lovelace (+15551234567) — online\nwork: Grace (+15559999999) — lastweek"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRunListAccountsUsesPythonFallbacks(t *testing.T) {
	lister := &fakeLister{
		labels: []string{"nameless", "phoneless", "statusless"},
		profiles: map[string]*mcpserver.SelfProfile{
			"nameless":   {Phone: "15550000000", StatusType: "UserStatusOffline"},
			"phoneless":  {FirstName: "Solo", StatusType: "UserStatusOffline"},
			"statusless": {FirstName: "No", LastName: "Status", Phone: "15550000001"},
		},
	}

	got, err := runListAccounts(mcpserver.WithAccountLister(context.Background(), lister))
	if err != nil {
		t.Fatalf("runListAccounts returned error: %v", err)
	}

	for _, want := range []string{
		"nameless: Unknown (+15550000000) — offline",
		"phoneless: Solo (+N/A) — offline",
		"statusless: No Status (+15550000001) — unknown",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing line %q in:\n%s", want, got)
		}
	}
}

func TestRunListAccountsDegradesFailedProfileToPlaceholder(t *testing.T) {
	lister := &fakeLister{
		labels:   []string{"broken", "healthy"},
		profiles: map[string]*mcpserver.SelfProfile{"healthy": {FirstName: "Ok", Phone: "1", StatusType: "UserStatusOffline"}},
		errs:     map[string]error{"broken": errors.New("auth key revoked")},
	}

	got, err := runListAccounts(mcpserver.WithAccountLister(context.Background(), lister))
	if err != nil {
		t.Fatalf("a per-account failure must not fail the call: %v", err)
	}

	if !strings.Contains(got, "broken: (unable to fetch profile)") {
		t.Errorf("missing failure placeholder in:\n%s", got)
	}
	if !strings.Contains(got, "healthy: Ok (+1) — offline") {
		t.Errorf("healthy account must still render in:\n%s", got)
	}
	if strings.Contains(got, "auth key revoked") {
		t.Error("the underlying error text must not leak into the tool result")
	}
}

func TestRunListAccountsWithoutListerReturnsFunnelCode(t *testing.T) {
	got, err := runListAccounts(context.Background())
	if err != nil {
		t.Fatalf("missing lister must be reported as a formatted string, not an error: %v", err)
	}
	if !strings.Contains(got, "GEN-ERR-") {
		t.Fatalf("got %q, want the funnel's stable error code", got)
	}
	if strings.Contains(got, "no Telegram accounts are connected") {
		t.Error("the funnel must not leak the internal cause into the tool result")
	}
}

func TestProfileStatusStripsPackageAndPrefix(t *testing.T) {
	cases := map[string]string{
		"UserStatusOnline":             "online",
		"*telegram.UserStatusLastWeek": "lastweek",
		"telegram.UserStatusRecently":  "recently",
		"":                             "unknown",
		"*telegram.UserStatusEmpty":    "empty",
	}
	for input, want := range cases {
		profile := &mcpserver.SelfProfile{StatusType: input}
		if got := profileStatus(profile); got != want {
			t.Errorf("profileStatus(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestProfileDisplayNameSanitizesNewlines(t *testing.T) {
	profile := &mcpserver.SelfProfile{FirstName: "Ada\nLovelace"}
	got := profileDisplayName(profile)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("display name must be single-lined, got %q", got)
	}
	if got != "Ada Lovelace" {
		t.Fatalf("got %q, want %q", got, "Ada Lovelace")
	}
}
