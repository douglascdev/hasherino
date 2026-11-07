package hasherino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/browser"
)

type TwitchOAuth struct {
	state string // Field from implicit grant flow used to prevent CSRF attacks
}

func NewTwitchOAuth() *TwitchOAuth {
	return &TwitchOAuth{
		state: strconv.Itoa(rand.Intn(100_000_000)),
	}
}

func (t *TwitchOAuth) IsTokenValid(token string) bool {
	req, err := http.NewRequest("GET", "https://id.twitch.tv/oauth2/validate", nil)
	if err != nil {
		log.Printf("Failed to create request for token validation: %s", err)
		return false
	}

	req.Header.Add("Authorization", "OAuth "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to validate token: %s", err)
		return false
	}

	log.Printf("Token validation status code: %d", resp.StatusCode)
	return resp.StatusCode == 200
}

func (t *TwitchOAuth) OpenOAuthPage(app_id string) {
	headers := map[string]string{
		"client_id":     app_id,
		"redirect_uri":  "http://localhost:17563",
		"response_type": "token",
		"scope":         "chat:edit chat:read user:manage:chat_color user:read:emotes",
		"state":         t.state,
	}
	headersStr := ""
	for header, value := range headers {
		headersStr += header + "=" + value
		headersStr += "&"
	}
	browser.OpenURL("https://id.twitch.tv/oauth2/authorize?" + headersStr[:len(headersStr)-1])
}

func (t *TwitchOAuth) ListenForOAuthRedirect(hc *HasherinoController) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Sends URL parameters passed by twitch as fragments(readable client-side only) to the auth route
		html := `
<!DOCTYPE html>
<html>
<head>
    <title>Hasherino Token</title>
</head>
<body>
    <div id="result"></div>

    <script>
        function extractFragmentParams() {
            const fragment = window.location.hash.substr(1);
            const params = new URLSearchParams(fragment);
            
            const accessToken = params.get('access_token');
            const state = params.get('state');
            
            if (accessToken && state) {
                const apiUrl = "http://localhost:17563/auth?access_token=" + accessToken + "&" + "state=" + state;
                fetch(apiUrl)
                    .then(response => response.text())
                    .then(result => {
                        // Display the fetched result in the HTML
                        const resultDiv = document.getElementById('result');
                        resultDiv.textContent = result;
                    })
                    .catch(error => console.error('Error:', error));
            } else {
                console.log('Access token or state not found in fragment.');
            }
        }
        
        extractFragmentParams();
    </script>
</body>
</html>
    `
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write([]byte(html))
	})
	http.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != t.state {
			w.WriteHeader(400)
			w.Write([]byte("Invalid state"))
			log.Printf("Invalid state, expected: %s, got: %s", t.state, r.URL.Query().Get("state"))
			return
		}
		token := r.URL.Query().Get("access_token")
		if token == "" {
			w.WriteHeader(400)
			w.Write([]byte("Missing access token"))
			log.Println("Missing access token")
			return
		}
		helix := NewHelix(hc.appId)
		users, err := helix.GetUsers(token, []string{})
		if err != nil || len(users.Data) != 1 {
			w.WriteHeader(500)
			w.Write([]byte("Failed to get user id and login, please try again"))
			log.Printf("Failed to get user id and login: %s", err)
			return
		}

		err = hc.AddAccount(users.Data[0].ID, users.Data[0].Login, token)
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte("Failed to add account"))
			log.Printf("Failed to add account: %s", err)
			return
		}
		if err := hc.Listen(); err != nil {
			log.Printf("Failed to connect after adding account: %s", err)
		}

		w.WriteHeader(200)
		w.Write([]byte("Account added, refresh your accounts list."))
	})

	err := http.ListenAndServe(":17563", nil)
	if err != nil {
		log.Fatal("OAuth listener failed", err)
	}
}

// Device Code Grant support.
//
// The implicit grant flow above is kept for clients that still use it. Device
// Code Grant is meant for public clients that cannot keep a client secret or
// receive a redirect, such as mobile apps.
const (
	TwitchOAuthScopes   = "chat:edit chat:read user:manage:chat_color user:read:emotes"
	deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
)

var (
	ErrDeviceAuthorizationPending = errors.New("authorization_pending")
	ErrDeviceSlowDown             = errors.New("slow_down")
	ErrDeviceAccessDenied         = errors.New("access_denied")
	ErrDeviceCodeExpired          = errors.New("expired_token")
)

// DeviceAuthorization is the response from Twitch's device authorization
// endpoint. The user enters UserCode at VerificationURI while the client polls
// for a token with DeviceCode.
type DeviceAuthorization struct {
	DeviceCode      string `json:"device_code"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
}

// DeviceToken is the access/refresh token pair returned by the device flow.
type DeviceToken struct {
	AccessToken  string   `json:"access_token"`
	ExpiresIn    int      `json:"expires_in"`
	RefreshToken string   `json:"refresh_token"`
	Scope        []string `json:"scope"`
	TokenType    string   `json:"token_type"`
}

type twitchOAuthError struct {
	Status    int    `json:"status"`
	Message   string `json:"message"`
	ErrorCode string `json:"error"`
}

func (e *twitchOAuthError) Error() string {
	message := e.Message
	if message == "" {
		message = e.ErrorCode
	}
	if message == "" {
		message = "unknown error"
	}
	if e.Status != 0 {
		return fmt.Sprintf("twitch oauth: %s (status %d)", message, e.Status)
	}
	return "twitch oauth: " + message
}

// StartDeviceAuth asks Twitch for a device and user code. Show the returned
// UserCode and VerificationURI to the user, then poll with WaitForDeviceAuth.
func (t *TwitchOAuth) StartDeviceAuth(ctx context.Context, clientID, scopes string) (*DeviceAuthorization, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scopes", scopes)

	var authorization DeviceAuthorization
	if err := t.postOAuthForm(ctx, "https://id.twitch.tv/oauth2/device", form, &authorization); err != nil {
		return nil, err
	}
	if authorization.DeviceCode == "" || authorization.UserCode == "" {
		return nil, errors.New("twitch oauth: device authorization response is missing a code")
	}
	return &authorization, nil
}

// CheckDeviceAuth performs one token poll. It returns
// ErrDeviceAuthorizationPending while the user has not approved the request.
func (t *TwitchOAuth) CheckDeviceAuth(ctx context.Context, clientID, scopes, deviceCode string) (*DeviceToken, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scopes", scopes)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", deviceCodeGrantType)

	var token DeviceToken
	if err := t.postOAuthForm(ctx, "https://id.twitch.tv/oauth2/token", form, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// WaitForDeviceAuth polls until the user approves the request, the code
// expires, or ctx is cancelled.
func (t *TwitchOAuth) WaitForDeviceAuth(ctx context.Context, clientID, scopes, deviceCode string, interval time.Duration) (*DeviceToken, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			token, err := t.CheckDeviceAuth(ctx, clientID, scopes, deviceCode)
			switch {
			case err == nil:
				return token, nil
			case errors.Is(err, ErrDeviceAuthorizationPending):
			case errors.Is(err, ErrDeviceSlowDown):
				interval += 5 * time.Second
			default:
				return nil, err
			}
			timer.Reset(interval)
		}
	}
}

// RefreshDeviceToken exchanges a device-flow refresh token for a new access
// token. Public clients do not send a client secret. The returned refresh
// token is normally rotated, so callers must persist it.
func (t *TwitchOAuth) RefreshDeviceToken(ctx context.Context, clientID, refreshToken string) (*DeviceToken, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	var token DeviceToken
	if err := t.postOAuthForm(ctx, "https://id.twitch.tv/oauth2/token", form, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func (t *TwitchOAuth) postOAuthForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		var oauthErr twitchOAuthError
		_ = json.Unmarshal(body, &oauthErr)
		if oauthErr.Status == 0 {
			oauthErr.Status = resp.StatusCode
		}
		switch oauthErr.Message {
		case ErrDeviceAuthorizationPending.Error():
			return ErrDeviceAuthorizationPending
		case ErrDeviceSlowDown.Error():
			return ErrDeviceSlowDown
		case ErrDeviceAccessDenied.Error():
			return ErrDeviceAccessDenied
		case ErrDeviceCodeExpired.Error():
			return ErrDeviceCodeExpired
		}
		if oauthErr.Message == "" && oauthErr.ErrorCode == "" {
			oauthErr.Message = resp.Status
		}
		return &oauthErr
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("twitch oauth: decode response: %w", err)
	}
	return nil
}

type Helix struct {
	appId string
}

func NewHelix(appId string) *Helix {
	return &Helix{
		appId: appId,
	}
}

type HelixUsers struct {
	// https://mholt.github.io/json-to-go/
	Data []struct {
		ID              string    `json:"id"`
		Login           string    `json:"login"`
		DisplayName     string    `json:"display_name"`
		Type            string    `json:"type"`
		BroadcasterType string    `json:"broadcaster_type"`
		Description     string    `json:"description"`
		ProfileImageURL string    `json:"profile_image_url"`
		OfflineImageURL string    `json:"offline_image_url"`
		ViewCount       int       `json:"view_count"`
		Email           string    `json:"email"`
		CreatedAt       time.Time `json:"created_at"`
	} `json:"data"`
}

func (h *Helix) GetUsers(token string, usernames []string) (*HelixUsers, error) {
	url := "https://api.twitch.tv/helix/users"

	params := ""
	for _, username := range usernames {
		params += "login=" + username
		params += "&"
	}
	if len(usernames) > 0 {
		params = params[:len(params)-1]
	}

	req, err := http.NewRequest("GET", url+"?"+params, nil)
	if err != nil {
		log.Printf("Failed to create request for helix users: %s Params: %s", err, params)
		return nil, err
	}

	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("Client-Id", h.appId)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to get helix users: %s", err)
		return nil, err
	}
	log.Printf("Helix users status code: %d", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body: %s", err)
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("helix users request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var users HelixUsers
	if err := json.Unmarshal(body, &users); err != nil {
		log.Printf("Failed to unmarshal response body: %s", err)
		return nil, err
	}

	return &users, nil
}

// HelixChatBadges is a set of chat badges from the helix chat badges
// endpoints.
type HelixChatBadges struct {
	Data []struct {
		SetID    string `json:"set_id"`
		Versions []struct {
			ID          string `json:"id"`
			ImageURL1x  string `json:"image_url_1x"`
			ImageURL2x  string `json:"image_url_2x"`
			ImageURL4x  string `json:"image_url_4x"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"versions"`
	} `json:"data"`
}

// GetChatBadges returns the badge image URLs keyed by badge set and version.
// A blank broadcasterID requests the global badge sets, otherwise the channel's
// badge sets are requested.
func (h *Helix) GetChatBadges(token string, broadcasterID string) (map[string]map[string]string, error) {
	requestURL := "https://api.twitch.tv/helix/chat/badges/global"
	if broadcasterID != "" {
		requestURL = "https://api.twitch.tv/helix/chat/badges?broadcaster_id=" + url.QueryEscape(broadcasterID)
	}

	req, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		log.Printf("Failed to create request for helix chat badges: %s", err)
		return nil, err
	}
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("Client-Id", h.appId)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to get helix chat badges: %s", err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read helix chat badges response body: %s", err)
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("helix chat badges request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var badges HelixChatBadges
	if err := json.Unmarshal(body, &badges); err != nil {
		log.Printf("Failed to unmarshal helix chat badges response body: %s", err)
		return nil, err
	}

	sets := make(map[string]map[string]string, len(badges.Data))
	for _, set := range badges.Data {
		if set.SetID == "" || len(set.Versions) == 0 {
			continue
		}
		versions := make(map[string]string, len(set.Versions))
		for _, version := range set.Versions {
			imageURL := version.ImageURL2x
			if imageURL == "" {
				imageURL = version.ImageURL1x
			}
			if imageURL == "" {
				imageURL = version.ImageURL4x
			}
			if version.ID != "" && imageURL != "" {
				versions[version.ID] = imageURL
			}
		}
		if len(versions) > 0 {
			sets[set.SetID] = versions
		}
	}
	return sets, nil
}

// GetUserForToken returns the id and login of the account that owns token.
func (t *TwitchOAuth) GetUserForToken(clientID, token string) (id, login string, err error) {
	users, err := NewHelix(clientID).GetUsers(token, nil)
	if err != nil {
		return "", "", err
	}
	if len(users.Data) != 1 {
		return "", "", errors.New("twitch oauth: could not determine user for token")
	}
	return users.Data[0].ID, users.Data[0].Login, nil
}

type ChatMessagesJson struct {
	Messages  []string `json:"messages"`
	Error     any      `json:"error"`
	ErrorCode any      `json:"error_code"`
}

func GetChatHistory(channel string, limit int) (*[]ChatMessage, error) {
	url := "https://recent-messages.robotty.de/api/v2/recent-messages/"
	url += channel
	url += "?limit=" + strconv.Itoa(limit)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("Failed to create request for chat history: %s", err)
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to get chat history: %s", err)
		return nil, err
	}
	log.Printf("Chat history status code: %d", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body: %s", err)
		return nil, err
	}

	var messagesJson ChatMessagesJson
	if err := json.Unmarshal(body, &messagesJson); err != nil {
		log.Printf("Failed to unmarshal response body: %s", err)
		return nil, err
	}

	if messagesJson.Error != nil {
		e := fmt.Sprintf("Failed to get chat history: %s", messagesJson.Error)
		log.Println(e)
		return nil, errors.New(e)
	}

	messages := []ChatMessage{}
	for _, messageStr := range messagesJson.Messages {
		msg, err := ParseMessage(messageStr)
		if err != nil {
			return nil, err
		}
		messages = append(messages, *msg)
	}

	return &messages, nil
}

// HelixUserEmote is one emote the user can use, like a global emote or one
// of its subscriptions.
type HelixUserEmote struct {
	EmoteSetID string   `json:"emote_set_id"`
	EmoteType  string   `json:"emote_type"`
	Format     []string `json:"format"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	OwnerID    string   `json:"owner_id"`
	Scale      []string `json:"scale"`
	ThemeMode  []string `json:"theme_mode"`
}

// Animated reports whether the emote has an animated format.
func (e *HelixUserEmote) Animated() bool {
	return slices.Contains(e.Format, "animated")
}

// HelixUserEmotes is one page of the emotes a user can use.
type HelixUserEmotes struct {
	Data       []HelixUserEmote `json:"data"`
	Template   string           `json:"template"`
	Pagination struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
}

// maxUserEmotePages limits how many pages of user emotes are followed, so a
// broken cursor cannot loop the requests forever.
const maxUserEmotePages = 10

// GetUserEmotes returns every emote the user can use, like global emotes and
// the emotes of its subscriptions, following the pagination of the helix
// chat emotes endpoint.
func (h *Helix) GetUserEmotes(token string, userId string) (*[]HelixUserEmote, error) {
	emotes := []HelixUserEmote{}
	cursor := ""
	for range maxUserEmotePages {
		requestUrl := "https://api.twitch.tv/helix/chat/emotes/user?user_id=" + userId
		if cursor != "" {
			requestUrl += "&after=" + url.QueryEscape(cursor)
		}

		req, err := http.NewRequest("GET", requestUrl, nil)
		if err != nil {
			log.Printf("Failed to create request for the emotes of user %s with error %s", userId, err)
			return nil, err
		}
		req.Header.Add("Authorization", "Bearer "+token)
		req.Header.Add("Client-Id", h.appId)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("Failed to get the emotes of user %s with error %s", userId, err)
			return nil, err
		}
		log.Printf("User emotes status code: %d", resp.StatusCode)
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			log.Printf("Failed to read response body: %s", readErr)
			return nil, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("failed to get the emotes of user %s: %s", userId, resp.Status)
		}

		var emotesPage HelixUserEmotes
		if err := json.Unmarshal(body, &emotesPage); err != nil {
			log.Printf("Failed to unmarshal response body: %s", err)
			return nil, err
		}
		emotes = append(emotes, emotesPage.Data...)
		cursor = emotesPage.Pagination.Cursor
		if cursor == "" {
			break
		}
	}
	return &emotes, nil
}

// STVFile is one file variant of a 7tv emote, e.g. its 2x webp.
type STVFile struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// STVHost holds the CDN files of a 7tv emote.
type STVHost struct {
	URL   string    `json:"url"`
	Files []STVFile `json:"files"`
}

// STVEmoteData is the emote data of a 7tv emote set entry.
type STVEmoteData struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Animated bool    `json:"animated"`
	Listed   bool    `json:"listed"`
	Host     STVHost `json:"host"`
}

// Dimensions returns the size of the smallest file of the emote, its
// intrinsic size that defines the aspect ratio it renders at.
func (d *STVEmoteData) Dimensions() (width int, height int) {
	for _, file := range d.Host.Files {
		if file.Width <= 0 || file.Height <= 0 {
			continue
		}
		if width == 0 || file.Width*file.Height < width*height {
			width, height = file.Width, file.Height
		}
	}
	return width, height
}

// EmoteSetEmote is one entry of a 7tv emote set.
type EmoteSetEmote struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	Data STVEmoteData `json:"data"`
}

type EmoteSet struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Emotes []EmoteSetEmote `json:"emotes"`
}

type STVUserJson struct {
	Data struct {
		UserByConnection struct {
			ID          string     `json:"id"`
			Username    string     `json:"username"`
			DisplayName string     `json:"display_name"`
			CreatedAt   time.Time  `json:"created_at"`
			AvatarURL   string     `json:"avatar_url"`
			Biography   string     `json:"biography"`
			EmoteSets   []EmoteSet `json:"emote_sets"`
			Connections []struct {
				Platform   string `json:"platform"`
				EmoteSetID string `json:"emote_set_id"`
			} `json:"connections"`
		} `json:"userByConnection"`
	} `json:"data"`
}

func (s *STVUserJson) DefaultEmoteSet() (*EmoteSet, error) {
	var defaultSetId *string = nil
	for _, connection := range s.Data.UserByConnection.Connections {
		if connection.Platform == "TWITCH" {
			defaultSetId = &connection.EmoteSetID
			break
		}
	}
	if defaultSetId == nil {
		return nil, errors.New("Failed to find twitch connection")
	}

	for _, emoteSet := range s.Data.UserByConnection.EmoteSets {
		if emoteSet.ID == *defaultSetId {
			return &emoteSet, nil
		}
	}
	return nil, errors.New("Failed to find default emote set")
}

func STVGetUser(userId string) (*STVUserJson, error) {
	query := map[string]interface{}{
		"operationName": "GetUserByConnection",
		"query": `
        query GetUserByConnection($platform: ConnectionPlatform! $id: String!) {
            userByConnection (platform: $platform id: $id) {
                id
        				username
        				display_name
        				created_at
        				avatar_url
        				biography
        				emote_sets {
            				id
            				name
            				emotes {
            						id
            						name
            						data {
                						id
                    					name
                    					animated
                    					listed
                    					host {
                    							files {
                    									name
                    									width
                    									height
                    							}
                    					}
                					}
            				}
        				}
        					connections {
            				platform
            				emote_set_id
        				}
      		}
  		}`,
		"variables": map[string]interface{}{
			"platform": "TWITCH",
			"id":       userId,
		},
	}

	jsonData, err := json.Marshal(query)
	if err != nil {
		fmt.Println("Error marshalling query:", err)
		return nil, err
	}

	req, err := http.NewRequest("POST", "https://7tv.io/v3/gql", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Failed to create request for 7tv user %s with error %s", userId, err)
		return nil, err
	}
	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to get 7tv user %s with error %s", userId, err)
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body: %s", err)
		return nil, err
	}

	var stvUserJson STVUserJson
	if err := json.Unmarshal(body, &stvUserJson); err != nil {
		log.Printf("Failed to unmarshal response body: %s", err)
		return nil, err
	}

	return &stvUserJson, nil
}

type STVGlobalEmotesJson struct {
	Data struct {
		EmoteSet struct {
			ID     string          `json:"id"`
			Name   string          `json:"name"`
			Emotes []EmoteSetEmote `json:"emotes"`
		} `json:"emoteSet"`
	} `json:"data"`
}

var cachedStvGlobalEmotes *STVGlobalEmotesJson = nil

func STVGetGlobalEmotes() (*STVGlobalEmotesJson, error, bool) {
	if cachedStvGlobalEmotes != nil {
		return cachedStvGlobalEmotes, nil, true
	}

	query := map[string]interface{}{
		"operationName": "EmoteSet",
		"query": `
        query EmoteSet {
    			emoteSet(id: "63584dd8848525f4dd62b143") {
        			id
        			name
        			emotes {
            			id
            			name
            			data {
                			id
                			name
                			animated
                			listed
                			host {
                					files {
                							name
                							width
                							height
                					}
                			}
            			}
        			}
    			}
			}
`,
		"variables": map[string]interface{}{},
	}

	jsonData, err := json.Marshal(query)
	if err != nil {
		fmt.Println("Error marshalling query:", err)
		return nil, err, false
	}

	req, err := http.NewRequest("POST", "https://7tv.io/v3/gql", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Failed to create request for 7tv global emotes: %s", err)
		return nil, err, false
	}
	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Failed to get 7tv global emotes: %s", err)
		return nil, err, false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body: %s", err)
		return nil, err, false
	}

	var stvGlobalEmotes STVGlobalEmotesJson
	if err := json.Unmarshal(body, &stvGlobalEmotes); err != nil {
		log.Printf("Failed to unmarshal response body: %s", err)
		return nil, err, false
	}

	cachedStvGlobalEmotes = &stvGlobalEmotes

	return &stvGlobalEmotes, nil, false
}
