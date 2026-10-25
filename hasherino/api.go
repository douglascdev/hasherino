package hasherino

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
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
		"scope":         "chat:edit chat:read user:manage:chat_color",
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

	var users HelixUsers
	if err := json.Unmarshal(body, &users); err != nil {
		log.Printf("Failed to unmarshal response body: %s", err)
		return nil, err
	}

	return &users, nil
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

type EmoteSet struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Emotes []struct {
		Name string `json:"name"`
		Data struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Animated bool   `json:"animated"`
			Listed   bool   `json:"listed"`
		} `json:"data"`
	} `json:"emotes"`
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
    										name
                				data {
                    				id
                    				name
                    				animated
                    				listed
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
			ID     string `json:"id"`
			Name   string `json:"name"`
			Emotes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Data struct {
					ID       string `json:"id"`
					Name     string `json:"name"`
					Animated bool   `json:"animated"`
					Listed   bool   `json:"listed"`
				} `json:"data"`
			} `json:"emotes"`
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
