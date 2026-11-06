package hasherino

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPostOAuthFormSendsDeviceFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected content type %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("client_id"); got != "client" {
			t.Errorf("expected client id, got %q", got)
		}
		if got := r.Form.Get("scopes"); got != TwitchOAuthScopes {
			t.Errorf("expected scopes, got %q", got)
		}
		if got := r.Form.Get("device_code"); got != "device" {
			t.Errorf("expected device code, got %q", got)
		}
		if got := r.Form.Get("grant_type"); got != deviceCodeGrantType {
			t.Errorf("expected device grant type, got %q", got)
		}

		json.NewEncoder(w).Encode(DeviceToken{AccessToken: "token", RefreshToken: "refresh"})
	}))
	defer server.Close()

	form := url.Values{}
	form.Set("client_id", "client")
	form.Set("scopes", TwitchOAuthScopes)
	form.Set("device_code", "device")
	form.Set("grant_type", deviceCodeGrantType)

	var token DeviceToken
	if err := NewTwitchOAuth().postOAuthForm(context.Background(), server.URL, form, &token); err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "token" || token.RefreshToken != "refresh" {
		t.Errorf("unexpected token: %+v", token)
	}
}

func TestPostOAuthFormMapsDeviceErrors(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    error
	}{
		{name: "pending", message: "authorization_pending", want: ErrDeviceAuthorizationPending},
		{name: "slow down", message: "slow_down", want: ErrDeviceSlowDown},
		{name: "denied", message: "access_denied", want: ErrDeviceAccessDenied},
		{name: "expired", message: "expired_token", want: ErrDeviceCodeExpired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{
					"status":  http.StatusBadRequest,
					"message": test.message,
				})
			}))
			defer server.Close()

			err := NewTwitchOAuth().postOAuthForm(context.Background(), server.URL, url.Values{}, &DeviceToken{})
			if !errors.Is(err, test.want) {
				t.Errorf("expected %v, got %v", test.want, err)
			}
		})
	}
}

func TestPostOAuthFormReturnsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"status":  http.StatusBadRequest,
			"message": "invalid client",
		})
	}))
	defer server.Close()

	err := NewTwitchOAuth().postOAuthForm(context.Background(), server.URL, url.Values{}, &DeviceToken{})
	var oauthErr *twitchOAuthError
	if !errors.As(err, &oauthErr) {
		t.Fatalf("expected a twitchOAuthError, got %T: %v", err, err)
	}
	if oauthErr.Message != "invalid client" || oauthErr.Status != http.StatusBadRequest {
		t.Errorf("unexpected error: %+v", oauthErr)
	}
}
