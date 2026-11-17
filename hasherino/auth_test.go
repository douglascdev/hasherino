package hasherino

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestRefreshActiveAccountTokenPersistsRotatedTokens(t *testing.T) {
	hc := newSearchEmoteController(t)
	expired := time.Now().Add(-time.Minute)
	if err := hc.permDB.Create(&Account{
		Id:             "account",
		Login:          "login",
		Token:          "old-token",
		RefreshToken:   "refresh-1",
		Active:         true,
		TokenExpiresAt: expired,
	}).Error; err != nil {
		t.Fatal(err)
	}

	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse the token request: %s", err)
			return
		}
		form = r.PostForm
		json.NewEncoder(w).Encode(DeviceToken{
			AccessToken:  "new-token",
			RefreshToken: "refresh-2",
			ExpiresIn:    3600,
			TokenType:    "bearer",
		})
	}))
	defer server.Close()

	oldEndpoint := twitchTokenEndpoint
	twitchTokenEndpoint = server.URL
	t.Cleanup(func() { twitchTokenEndpoint = oldEndpoint })

	account := &Account{}
	if err := hc.permDB.Take(account, "id = ?", "account").Error; err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if err := hc.refreshActiveAccountToken(context.Background(), account); err != nil {
		t.Fatalf("refreshActiveAccountToken failed: %v", err)
	}

	if got := form.Get("grant_type"); got != "refresh_token" {
		t.Errorf("unexpected grant type: got %q", got)
	}
	if got := form.Get("refresh_token"); got != "refresh-1" {
		t.Errorf("unexpected refresh token: got %q", got)
	}
	if account.Token != "new-token" || account.RefreshToken != "refresh-2" {
		t.Errorf("expected the account to use the rotated tokens, got token %q refresh %q", account.Token, account.RefreshToken)
	}
	if !account.TokenExpiresAt.After(before.Add(59 * time.Minute)) {
		t.Errorf("expected the expiry to be about an hour from now, got %s", account.TokenExpiresAt)
	}

	stored := &Account{}
	if err := hc.permDB.Take(stored, "id = ?", "account").Error; err != nil {
		t.Fatal(err)
	}
	if stored.Token != "new-token" || stored.RefreshToken != "refresh-2" {
		t.Errorf("expected the rotated tokens to be persisted, got token %q refresh %q", stored.Token, stored.RefreshToken)
	}
	if !stored.TokenExpiresAt.Equal(account.TokenExpiresAt) {
		t.Errorf("expected the persisted expiry to match, got %s and %s", stored.TokenExpiresAt, account.TokenExpiresAt)
	}
}

func TestRefreshActiveAccountTokenRequiresRefreshToken(t *testing.T) {
	hc := newSearchEmoteController(t)
	if err := hc.permDB.Create(&Account{Id: "account", Login: "login", Token: "token", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	account := &Account{}
	if err := hc.permDB.Take(account, "id = ?", "account").Error; err != nil {
		t.Fatal(err)
	}
	if err := hc.refreshActiveAccountToken(context.Background(), account); err != errNoRefreshToken {
		t.Errorf("expected errNoRefreshToken, got %v", err)
	}
}

func TestUpsertAccountWithTokenPersistsRefreshToken(t *testing.T) {
	hc := newSearchEmoteController(t)
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := hc.UpsertAccountWithToken("account", "login", "access", "refresh", expiry); err != nil {
		t.Fatalf("UpsertAccountWithToken failed: %v", err)
	}

	account, err := hc.GetActiveAccount()
	if err != nil {
		t.Fatal(err)
	}
	if account.Token != "access" || account.RefreshToken != "refresh" {
		t.Errorf("unexpected tokens: %+v", account)
	}
	if !account.TokenExpiresAt.Equal(expiry) {
		t.Errorf("unexpected expiry: got %s, want %s", account.TokenExpiresAt, expiry)
	}

	// re-authorizing without a refresh token must not wipe the stored one
	if err := hc.UpsertAccount("account", "login", "access-2"); err != nil {
		t.Fatalf("UpsertAccount failed: %v", err)
	}
	account, err = hc.GetActiveAccount()
	if err != nil {
		t.Fatal(err)
	}
	if account.Token != "access-2" {
		t.Errorf("expected the access token to update, got %q", account.Token)
	}
	if account.RefreshToken != "refresh" {
		t.Errorf("expected the refresh token to be preserved, got %q", account.RefreshToken)
	}
	if !account.TokenExpiresAt.Equal(expiry) {
		t.Errorf("expected the expiry to be preserved, got %s", account.TokenExpiresAt)
	}
}

func TestSendMessageWithoutSocketReturnsNotConnected(t *testing.T) {
	hc := newSearchEmoteController(t)
	if err := hc.SendMessage("channel", "hello"); err != ErrNotConnected {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}
