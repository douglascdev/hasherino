package hasherino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Controlls everything in the app. Called by UI code, making it UI library agnostic.
type HasherinoController struct {
	appId       string
	selectedTab string
	callbackMap map[string]func(ChatMessage)
	twitchOAuth *TwitchOAuth
	// socketLock guards the readWS and writeWS pointers. The sockets
	// themselves are safe for concurrent use, but reconnecting replaces the
	// pointer while the ui is sending.
	socketLock sync.RWMutex
	readWS     *TwitchChatWebsocket
	writeWS    *TwitchChatWebsocket
	// authRefreshLock serializes token refreshes, so a proactive refresh and
	// an authentication failure cannot rotate the token twice.
	authRefreshLock sync.Mutex
	// listenLock and writeConnectLock serialize connection setup, so two
	// callers cannot replace a socket and leak the other connection.
	listenLock       sync.Mutex
	writeConnectLock sync.Mutex
	memDB            *gorm.DB
	permDB           *gorm.DB
	// userEmotesLock and userEmotesLoadedId guard that the twitch emotes of
	// an account are only loaded once per run
	userEmotesLock     sync.Mutex
	userEmotesLoadedId string
	// emoteRefreshHook is notified when a refreshed emote list reports new or
	// changed emotes, so the image cache can update its files.
	emoteRefreshLock sync.RWMutex
	emoteRefreshHook func([]*Emote)
	// chatReconnectHook is notified when the reading socket reconnects after
	// a drop, so the chat can fetch the messages it missed.
	chatReconnectLock sync.RWMutex
	chatReconnectHook func()
}

const (
	defaultClientID = "hvmj7blkwy2gw3xf820n47i85g4sub"
	// tokenRefreshMargin is how long before expiry a token is refreshed, so
	// a reconnect never races the expiry.
	tokenRefreshMargin = 5 * time.Minute
	// tokenRefreshTimeout limits a token refresh request.
	tokenRefreshTimeout = 30 * time.Second
)

var errNoRefreshToken = errors.New("the account has no refresh token; re-authorize it")

func (hc *HasherinoController) New(callbackMap map[string]func(ChatMessage)) (*HasherinoController, error) {
	return hc.NewWithClientID(callbackMap, defaultClientID)
}

// NewWithClientID creates a controller that uses clientID for Helix requests.
// Tokens only work with the client ID that created them, so the client that
// minted the token must be passed here.
func (hc *HasherinoController) NewWithClientID(callbackMap map[string]func(ChatMessage), clientID string) (*HasherinoController, error) {
	writeWS := &TwitchChatWebsocket{}
	readWS := &TwitchChatWebsocket{}

	dataFolder, err := GetDataFolder()
	if err != nil {
		return nil, err
	}

	memDB, err := openMemDB()
	if err != nil {
		return nil, err
	}
	memDB.AutoMigrate(&TempTab{}, &ChatUser{}, &Emote{}, &ChatUserTempTab{})

	// ensure the parent directory exists
	err = os.MkdirAll(dataFolder, os.ModePerm)
	if err != nil {
		log.Fatalf("failed to create directory: %v", err)
	}

	// open the SQLite database safely
	permDB, err := gorm.Open(sqlite.Open(filepath.Join(dataFolder, "gorm.db")), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	permDB.AutoMigrate(&Account{}, &Tab{}, &AppSettings{}, &CachedEmote{})

	c := &HasherinoController{
		appId:       clientID,
		callbackMap: callbackMap,
		twitchOAuth: NewTwitchOAuth(),
		readWS:      readWS,
		writeWS:     writeWS,
		memDB:       memDB,
		permDB:      permDB,
	}
	settings := &AppSettings{}
	result := permDB.Take(settings)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		result = permDB.Create(&AppSettings{
			ChatMessageLimit: 100,
			ChatHistory:      DefaultChatHistory,
			ShowMessageTime:  DefaultShowMessageTime,
			EmoteSize:        DefaultEmoteSize,
			FontSize:         DefaultFontSize,
			SettingsVersion:  currentSettingsVersion,
		})
	} else if result.Error != nil {
		return nil, err
	}
	// settings created before the emote and font size settings existed have
	// them set to zero, so they need to be filled with the defaults
	missingDefaults := settings.EmoteSize == 0 || settings.FontSize == 0
	changed := missingDefaults
	if settings.EmoteSize == 0 {
		settings.EmoteSize = DefaultEmoteSize
	}
	if settings.FontSize == 0 {
		settings.FontSize = DefaultFontSize
	}
	if missingDefaults {
		settings.ShowMessageTime = DefaultShowMessageTime
	}
	if settings.SettingsVersion < currentSettingsVersion {
		if settings.SettingsVersion < 1 {
			// Chat history is on by default now; enable it once for
			// installations created before that change.
			settings.ChatHistory = DefaultChatHistory
		}
		settings.SettingsVersion = currentSettingsVersion
		changed = true
	}
	if changed {
		if err := permDB.Save(settings).Error; err != nil {
			log.Println("Failed to save default settings:", err)
		}
	}
	c.hydrateEmoteCatalog()
	go c.twitchOAuth.ListenForOAuthRedirect(c)
	return c, nil
}

// openMemDB opens the in memory database. It is limited to a single
// connection, because the shared cache of the in memory database locks tables
// for other connections while they are being written to, which made
// concurrent reads fail with "database table is locked".
func openMemDB() (*gorm.DB, error) {
	memDB, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := memDB.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	return memDB, nil
}

func (hc *HasherinoController) AddAccount(id string, login string, token string) error {
	return hc.permDB.Transaction(func(tx *gorm.DB) error {
		var activeCount int64
		if err := tx.Model(&Account{}).Where("Active = ?", true).Count(&activeCount).Error; err != nil {
			return err
		}
		active := activeCount == 0
		result := tx.Create(&Account{Id: id, Login: login, Token: token, Active: active})
		if result.Error != nil {
			return errors.New("could not create account")
		}
		// no errors, commit transaction
		return nil
	})
}

// UpsertAccount saves the account, creating it when needed. An existing
// account keeps its active state, unless no account is active, in which case
// this account becomes the active one. The refresh token and expiry of an
// existing account are preserved.
func (hc *HasherinoController) UpsertAccount(id string, login string, token string) error {
	return hc.upsertAccount(id, login, token, nil, nil)
}

// UpsertAccountWithToken saves the account including the refresh token and
// expiry returned by the device flow, so the access token can be refreshed
// later without asking the user again.
func (hc *HasherinoController) UpsertAccountWithToken(id string, login string, token string, refreshToken string, expiresAt time.Time) error {
	return hc.upsertAccount(id, login, token, &refreshToken, &expiresAt)
}

func (hc *HasherinoController) upsertAccount(id string, login string, token string, refreshToken *string, expiresAt *time.Time) error {
	return hc.permDB.Transaction(func(tx *gorm.DB) error {
		account := &Account{}
		result := tx.Take(account, "Id = ?", id)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			var activeCount int64
			if err := tx.Model(&Account{}).Where("Active = ?", true).Count(&activeCount).Error; err != nil {
				return err
			}
			active := activeCount == 0
			account = &Account{Id: id, Login: login, Token: token, Active: active}
			if refreshToken != nil {
				account.RefreshToken = *refreshToken
			}
			if expiresAt != nil {
				account.TokenExpiresAt = *expiresAt
			}
			return tx.Create(account).Error
		}
		if result.Error != nil {
			return result.Error
		}

		var activeCount int64
		if err := tx.Model(&Account{}).Where("Active = ?", true).Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount == 0 {
			account.Active = true
		}
		account.Login = login
		account.Token = token
		if refreshToken != nil && *refreshToken != "" {
			account.RefreshToken = *refreshToken
		}
		if expiresAt != nil {
			account.TokenExpiresAt = *expiresAt
		}
		return tx.Save(account).Error
	})
}

func (hc *HasherinoController) RemoveAccount(id string) error {
	return hc.permDB.Transaction(func(tx *gorm.DB) error {
		account := &Account{}
		result := tx.Take(&account, "Id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if account.Active {
			hc.ReconnectChat()
		}
		result = tx.Delete(&account)
		if result.Error != nil {
			return result.Error
		}
		return tx.Where("owner_id = ?", id).Delete(&CachedEmote{}).Error
	})
}

func (hc *HasherinoController) GetAccounts() ([]*Account, error) {
	accounts := []*Account{}
	result := hc.permDB.Find(&accounts)
	return accounts, result.Error
}

func (hc *HasherinoController) SetActiveAccount(id string) error {
	err := hc.permDB.Transaction(func(tx *gorm.DB) error {
		// Disable every active account(should be just one, but just in case)
		activeAccounts := []*Account{}
		result := tx.Find(&activeAccounts, "Active = ?", true)
		if result.Error != nil {
			return result.Error
		}
		if len(activeAccounts) > 0 {
			for _, account := range activeAccounts {
				account.Active = false
				result = tx.Save(&account)
				if result.Error != nil {
					return result.Error
				}
			}
		}
		// Set account as active
		account := &Account{}
		result = tx.Take(&account, "Id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		account.Active = true
		return tx.Save(&account).Error
	})
	if err != nil {
		return err
	}

	// make the emotes of the newly active account available right away
	hc.hydrateEmoteCatalog()

	hc.ReconnectChat()
	go func() {
		if err := hc.Listen(); err != nil {
			log.Printf("failed to connect after activating account: %s", err)
		}
	}()

	return nil
}

func (hc *HasherinoController) GetActiveAccount() (*Account, error) {
	account := &Account{}
	result := hc.permDB.Take(&account, "Active = ?", true)

	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, errors.New("no active account")
	}
	if result.Error != nil {
		return nil, result.Error
	}

	return account, nil
}

func (hc *HasherinoController) OpenOAuthPage() {
	hc.twitchOAuth.OpenOAuthPage(hc.appId)
}

func (hc *HasherinoController) AddTab(channel string) error {
	channel = strings.ToLower(strings.TrimSpace(channel))

	channelId := ""
	err := hc.permDB.Transaction(func(tx *gorm.DB) error {
		if readWS := hc.readSocket(); readWS != nil && readWS.HasChannel(channel) {
			return errors.New("already joined channel " + channel)
		}

		activeAccount := &Account{}
		result := tx.Take(&activeAccount, "Active = ?", true)
		if result.Error != nil {
			return errors.New("no active account")
		}

		helix := NewHelix(hc.appId)
		users, err := helix.GetUsers(activeAccount.Token, []string{channel})
		if err != nil || len(users.Data) != 1 {
			return fmt.Errorf("failed to obtain channel's id and login: %w", err)
		}

		login := strings.ToLower(users.Data[0].Login)
		tab := &Tab{
			Id:          users.Data[0].ID,
			Login:       login,
			DisplayName: users.Data[0].DisplayName,
			Selected:    false,
		}

		result = tx.Create(&tab)
		if result.Error != nil {
			return result.Error
		}

		if readWS := hc.readSocket(); readWS != nil {
			if err := readWS.Join(login); err != nil {
				// the channel is remembered anyway and the reader joins it
				// again after reconnecting
				log.Printf("failed to join channel %s: %s", login, err)
			}
		}

		channelId = users.Data[0].ID
		return nil
	})
	if err != nil {
		return err
	}

	// load the emotes after the tab transaction committed, so saving the
	// emote catalog cannot contend with its write lock
	return hc.AddTempTabs(&[]string{channelId})
}

type EmojiJsonMap map[string]struct {
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	Group           string `json:"group"`
	EmojiVersion    string `json:"emoji_version"`
	UnicodeVersion  string `json:"unicode_version"`
	SkinToneSupport bool   `json:"skin_tone_support"`
}

var cachedEmojiJson *EmojiJsonMap

func GetEmojiJSONMap() (*EmojiJsonMap, error) {
	if cachedEmojiJson != nil {
		return cachedEmojiJson, nil
	}

	file, err := os.Open(path.Join("..", "data-by-emoji.json"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	bytes, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	var emojiJson EmojiJsonMap
	err = json.Unmarshal(bytes, &emojiJson)
	if err != nil {
		return nil, err
	}
	cachedEmojiJson = &emojiJson
	return &emojiJson, nil
}

func (hc *HasherinoController) AddTempTabs(channelIds *[]string) error {
	go func(channelIds *[]string) {
		globalEmotes, err, isCached := STVGetGlobalEmotes()
		if !isCached {
			if err != nil {
				log.Printf("Failed to load global 7tv emotes: %s", err)
			} else {
				emoteObjs := []Emote{}
				for _, emote := range globalEmotes.Data.EmoteSet.Emotes {
					width, height := emote.Data.Dimensions()
					emoteObjs = append(emoteObjs, Emote{
						Id:        emote.ID,
						Source:    SevenTV,
						Name:      emote.Name,
						Animated:  emote.Data.Animated,
						Width:     width,
						Height:    height,
						Version:   emote.Data.Version(),
						ChannelID: nil,
						OwnerID:   "",
						Owner:     nil,
					})
				}
				log.Println("Loaded " + strconv.Itoa(len(emoteObjs)) + " 7tv global emotes")
				if len(emoteObjs) == 0 {
					// an empty global set is almost certainly a broken
					// response; keep the cached emotes instead of wiping them
					log.Println("7tv returned no global emotes; keeping the cached ones")
				} else if err := hc.replaceEmotes(emoteScope{source: SevenTV}, emoteObjs); err != nil {
					log.Printf("Failed to save 7tv global emotes: %s", err)
				}
			}
		}

		stvUsers := make(map[string]*STVUserJson)
		mutex := sync.Mutex{}
		wg := sync.WaitGroup{}
		for _, channelId := range *channelIds {
			wg.Add(1)
			go func(channelId string) {
				defer wg.Done()
				stvUser, err := STVGetUser(channelId)
				if err != nil {
					log.Printf("Failed to load 7tv user emotes: %s", err)
				} else {
					mutex.Lock()
					stvUsers[channelId] = stvUser
					mutex.Unlock()
				}
			}(channelId)
		}
		wg.Wait()

		for channelId, stvUser := range stvUsers {
			if channelId == "" {
				continue
			}

			if len(stvUser.Data.UserByConnection.EmoteSets) == 0 {
				// no sets means the connection is unknown, not that the
				// channel has no emotes; keep the cached ones
				continue
			}

			channelEmotes := []Emote{}
			emoteSet, err := stvUser.DefaultEmoteSet()
			if err != nil {
				log.Printf("Failed to load default 7tv emote set: %s", err)
				continue
			}
			for _, emote := range emoteSet.Emotes {
				width, height := emote.Data.Dimensions()
				channelEmotes = append(channelEmotes, Emote{
					Id:        emote.Data.ID,
					Source:    SevenTV,
					Name:      emote.Name,
					Animated:  emote.Data.Animated,
					Width:     width,
					Height:    height,
					Version:   emote.Data.Version(),
					ChannelID: &channelId,
					OwnerID:   "",
					Owner:     nil,
				})
			}
			log.Println("Loaded " + strconv.Itoa(len(channelEmotes)) + " 7tv user emotes for " + channelId)
			if err := hc.replaceEmotes(emoteScope{source: SevenTV, channelID: &channelId}, channelEmotes); err != nil {
				log.Printf("Failed to save 7tv user emotes for %s: %s", channelId, err)
			}
		}
	}(channelIds)

	err := hc.memDB.Transaction(func(tx *gorm.DB) error {
		var tempTabs []TempTab
		for _, channelId := range *channelIds {
			tempTabs = append(tempTabs, TempTab{
				Id:        channelId,
				ChatUsers: []ChatUser{},
			})
		}
		result := tx.Create(&tempTabs)
		if result.Error != nil {
			return result.Error
		}
		return nil
	})

	return err
}

// SetEmoteRefreshHook sets the function notified when a refreshed emote list
// adds, changes or removes emotes. It is called once per refreshed list, from
// background goroutines.
func (hc *HasherinoController) SetEmoteRefreshHook(hook func([]*Emote)) {
	hc.emoteRefreshLock.Lock()
	defer hc.emoteRefreshLock.Unlock()
	hc.emoteRefreshHook = hook
}

func (hc *HasherinoController) notifyEmotesChanged(emotes []Emote) {
	hc.emoteRefreshLock.RLock()
	hook := hc.emoteRefreshHook
	hc.emoteRefreshLock.RUnlock()
	if hook == nil || len(emotes) == 0 {
		return
	}
	copied := make([]*Emote, len(emotes))
	for i := range emotes {
		emote := emotes[i]
		copied[i] = &emote
	}
	hook(copied)
}

// emoteScope selects one emote list: the source of the emotes, their owner
// and the channel they belong to. An empty channelID selects the emotes that
// are not bound to a channel, like global 7tv emotes.
type emoteScope struct {
	source    EmoteSourceEnum
	ownerID   string
	channelID *string
}

func (s emoteScope) query(tx *gorm.DB) *gorm.DB {
	result := tx.Where("source = ? AND owner_id = ?", s.source, s.ownerID)
	if s.channelID == nil {
		return result.Where("channel_id IS NULL")
	}
	return result.Where("channel_id = ?", *s.channelID)
}

// replaceEmotes replaces every emote in scope with emotes. The persisted
// catalog is updated as well, and the refresh hook is notified about the
// emotes that were added, changed or removed. Replacing instead of inserting
// keeps renamed, modified and deleted emotes in sync with their list.
func (hc *HasherinoController) replaceEmotes(scope emoteScope, emotes []Emote) error {
	// 7tv sets can list the same emote more than once, for example under
	// several aliases; keep every alias but drop exact duplicate entries
	emotes = dedupeEmotes(emotes)

	previous := []Emote{}
	if err := scope.query(hc.memDB).Find(&previous).Error; err != nil {
		return err
	}
	previousByIdentity := make(map[string]*Emote, len(previous))
	for i := range previous {
		previousByIdentity[previous[i].identity()] = &previous[i]
	}
	current := make(map[string]struct{}, len(emotes))
	notify := []Emote{}
	for i := range emotes {
		identity := emotes[i].identity()
		current[identity] = struct{}{}
		// a new emote has no cached content to invalidate, so only changes
		// and removals are reported
		if old, ok := previousByIdentity[identity]; ok && !sameEmote(old, &emotes[i]) {
			notify = append(notify, emotes[i])
		}
	}
	for i := range previous {
		if _, ok := current[previous[i].identity()]; !ok {
			notify = append(notify, previous[i])
		}
	}

	err := hc.memDB.Transaction(func(tx *gorm.DB) error {
		if err := scope.query(tx).Delete(&Emote{}).Error; err != nil {
			return err
		}
		if len(emotes) == 0 {
			return nil
		}
		// DoNothing keeps two concurrent refreshes of the same list from
		// failing with a unique constraint error
		return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&emotes, 500).Error
	})
	if err != nil {
		return err
	}

	cached := make([]CachedEmote, len(emotes))
	for i := range emotes {
		cached[i] = cachedEmote(&emotes[i])
	}
	err = hc.permDB.Transaction(func(tx *gorm.DB) error {
		if err := scope.query(tx).Delete(&CachedEmote{}).Error; err != nil {
			return err
		}
		if len(cached) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&cached, 500).Error
	})
	if err != nil {
		// the in-memory list is already usable; the catalog catches up the
		// next time the list is refreshed
		log.Printf("Failed to update the cached emote catalog: %s", err)
	}

	hc.notifyEmotesChanged(notify)
	return nil
}

// dedupeEmotes drops exact duplicate entries, which a list can contain
// after a malformed or overlapping response.
func dedupeEmotes(emotes []Emote) []Emote {
	seen := make(map[string]struct{}, len(emotes))
	result := make([]Emote, 0, len(emotes))
	for i := range emotes {
		identity := emotes[i].identity()
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		result = append(result, emotes[i])
	}
	return result
}

// SetChatReconnectHook sets the function notified when the chat reader
// reconnects after a connection loss. It is called from the listener
// goroutine.
func (hc *HasherinoController) SetChatReconnectHook(hook func()) {
	hc.chatReconnectLock.Lock()
	defer hc.chatReconnectLock.Unlock()
	hc.chatReconnectHook = hook
}

func (hc *HasherinoController) notifyChatReconnected() {
	hc.chatReconnectLock.RLock()
	hook := hc.chatReconnectHook
	hc.chatReconnectLock.RUnlock()
	if hook != nil {
		hook()
	}
}

// sameEmote reports whether a refreshed emote still describes the same
// emote, so its cached file and rendering do not need to be updated.
func sameEmote(a, b *Emote) bool {
	return a.Name == b.Name &&
		a.Animated == b.Animated &&
		a.Width == b.Width &&
		a.Height == b.Height &&
		a.Version == b.Version
}

// hydrateEmoteCatalog fills the in-memory emote database with the persisted
// catalog of the active account, so chats can render cached emotes before
// the emote lists are fetched again.
func (hc *HasherinoController) hydrateEmoteCatalog() {
	owner := ""
	if account, err := hc.GetActiveAccount(); err == nil {
		owner = account.Id
	}

	cached := []CachedEmote{}
	query := hc.permDB.Where("owner_id = ?", "")
	if owner != "" {
		query = hc.permDB.Where("owner_id = ? OR owner_id = ?", owner, "")
	}
	if err := query.Find(&cached).Error; err != nil {
		log.Printf("Failed to load the cached emote catalog: %s", err)
		return
	}
	if len(cached) == 0 {
		return
	}

	emotes := make([]Emote, len(cached))
	for i := range cached {
		emotes[i] = cached[i].emote()
	}
	err := hc.memDB.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&emotes, 500).Error
	if err != nil {
		log.Printf("Failed to hydrate the cached emotes: %s", err)
		return
	}
	log.Println("Loaded " + strconv.Itoa(len(emotes)) + " cached emotes")
}

// GetEmotes returns every emote matching the search for the selected tab.
func (hc *HasherinoController) GetEmotes(search string) ([]*Emote, error) {
	return hc.SearchEmotes(search, 0)
}

// SearchEmotes returns emotes of every source matching the search for the
// selected tab. Exact matches come first, followed by prefix matches and
// then substring matches. A non-positive limit returns every match.
func (hc *HasherinoController) SearchEmotes(search string, limit int) ([]*Emote, error) {
	return hc.searchEmotes(nil, search, limit)
}

// SearchEmotesBySource returns emotes of the given source matching the
// search for the selected tab, like the twitch emotes of the emote picker.
// Exact matches come first, followed by prefix matches and then substring
// matches. A non-positive limit returns every match.
func (hc *HasherinoController) SearchEmotesBySource(source EmoteSourceEnum, search string, limit int) ([]*Emote, error) {
	return hc.searchEmotes(&source, search, limit)
}

func (hc *HasherinoController) searchEmotes(source *EmoteSourceEnum, search string, limit int) ([]*Emote, error) {
	emotes := []*Emote{}
	err := hc.memDB.Transaction(func(tx *gorm.DB) error {
		tab := &Tab{}
		result := hc.permDB.Take(&tab, "Selected = ?", true)
		if result.Error != nil {
			return result.Error
		}
		activeAccount := &Account{}
		result = hc.permDB.Take(&activeAccount, "Active = ?", true)
		if result.Error != nil {
			return result.Error
		}

		escaped := escapeLikePattern(search)
		query := `(owner_id = ? OR owner_id = '') AND (channel_id = ? OR channel_id IS NULL) AND name LIKE ? ESCAPE '\'`
		vars := []interface{}{activeAccount.Id, tab.Id, "%" + escaped + "%"}
		if source != nil {
			query += ` AND source = ?`
			vars = append(vars, *source)
		}
		order := clause.OrderBy{Expression: clause.Expr{
			SQL:  `CASE WHEN name = ? COLLATE NOCASE THEN 0 WHEN name LIKE ? ESCAPE '\' THEN 1 ELSE 2 END, name COLLATE NOCASE`,
			Vars: []interface{}{search, escaped + "%"},
		}}

		result = tx.Where(query, vars...).Order(order)
		if limit > 0 {
			result = result.Limit(limit)
		}
		result = result.Find(&emotes)
		log.Printf("Found %d emotes for tab %s (query %q, limit %d)", len(emotes), tab.Login, search, limit)
		if result.Error != nil {
			return result.Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return emotes, nil
}

func escapeLikePattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// GetChannelEmotes returns every emote that can be rendered in the given channel,
// regardless of the currently selected tab.
func (hc *HasherinoController) GetChannelEmotes(channel string) ([]*Emote, error) {
	tab := &Tab{}
	result := hc.permDB.Take(&tab, "Login = ? COLLATE NOCASE", channel)
	if result.Error != nil {
		return nil, result.Error
	}
	activeAccount := &Account{}
	result = hc.permDB.Take(&activeAccount, "Active = ?", true)
	if result.Error != nil {
		return nil, result.Error
	}

	emotes := []*Emote{}
	query := "(owner_id = ? OR owner_id = '') AND (channel_id = ? OR channel_id IS NULL)"
	result = hc.memDB.Where(query, activeAccount.Id, tab.Id).Find(&emotes)
	if result.Error != nil {
		return nil, result.Error
	}
	return emotes, nil
}

// GetChatBadges returns the badge image URLs available in the given channel,
// keyed by badge set and version. Global badges are merged with the channel's
// own badges, which take precedence for sets present in both.
func (hc *HasherinoController) GetChatBadges(channelID string) (map[string]map[string]string, error) {
	activeAccount, err := hc.GetActiveAccount()
	if err != nil {
		return nil, err
	}

	helix := NewHelix(hc.appId)
	badges, err := helix.GetChatBadges(activeAccount.Token, "")
	if err != nil {
		return nil, err
	}
	if channelID == "" {
		return badges, nil
	}

	channelBadges, err := helix.GetChatBadges(activeAccount.Token, channelID)
	if err != nil {
		// Global badges are still useful when the channel's cannot be loaded.
		log.Printf("failed to load channel badges for %s: %s", channelID, err)
		return badges, nil
	}
	for setID, versions := range channelBadges {
		merged := badges[setID]
		if merged == nil {
			merged = make(map[string]string, len(versions))
			badges[setID] = merged
		}
		for version, imageURL := range versions {
			merged[version] = imageURL
		}
	}
	return badges, nil
}

func (hc *HasherinoController) RemoveTab(id string) error {
	err := hc.permDB.Transaction(func(tx *gorm.DB) error {
		tab := &Tab{}
		result := tx.Take(&tab, "Id = ?", id)
		if result.Error != nil {
			return errors.New("tab not found for id " + id)
		}

		result = tx.Delete(&tab)
		if result.Error != nil {
			return result.Error
		}

		result = hc.memDB.Delete(&TempTab{}, "Id = ?", id)
		if result.Error != nil {
			return result.Error
		}

		// the emotes of the channel are no longer needed
		if result = hc.memDB.Where("channel_id = ?", id).Delete(&Emote{}); result.Error != nil {
			return result.Error
		}
		if result = tx.Where("channel_id = ?", id).Delete(&CachedEmote{}); result.Error != nil {
			return result.Error
		}

		if readWS := hc.readSocket(); readWS != nil {
			if err := readWS.Part(tab.Login); err != nil {
				// the channel is removed locally either way; the reader
				// reconnects without it
				log.Printf("failed to part channel %s: %s", tab.Login, err)
			}
		}

		return nil
	})
	return err
}

func (hc *HasherinoController) GetTabs() ([]*Tab, error) {
	tabs := []*Tab{}
	result := hc.permDB.Find(&tabs)
	return tabs, result.Error
}

func (hc *HasherinoController) SetSelectedTab(login string) error {
	login = strings.ToLower(strings.TrimSpace(login))

	return hc.permDB.Transaction(func(tx *gorm.DB) error {
		selectedTabs := []*Tab{}
		result := tx.Find(&selectedTabs, "Selected = ?", true)
		if result.Error != nil {
			return result.Error
		}
		if len(selectedTabs) > 0 {
			for _, tab := range selectedTabs {
				tab.Selected = false
				result = tx.Save(&tab)
				if result.Error != nil {
					return result.Error
				}
			}
		}
		tab := &Tab{}
		result = tx.Take(&tab, "Login = ? COLLATE NOCASE", login)
		if result.Error != nil {
			return result.Error
		}
		tab.Selected = true
		result = tx.Save(&tab)
		if result.Error != nil {
			return result.Error
		}
		return nil
	})
}

func (hc *HasherinoController) GetSelectedTab() (*Tab, error) {
	tab := &Tab{}
	result := hc.permDB.Take(&tab, "Selected = ?", true)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, errors.New("no tab selected")
	}
	return tab, result.Error
}

// loadUserEmotes loads the twitch emotes the account can use, like its
// global and subscription emotes, once per account per run. The emotes are
// owned by the account, so they only turn up in the searches of the account
// that can use them.
func (hc *HasherinoController) loadUserEmotes(account *Account) {
	hc.userEmotesLock.Lock()
	defer hc.userEmotesLock.Unlock()
	if hc.userEmotesLoadedId == account.Id {
		return
	}

	helix := NewHelix(hc.appId)
	userEmotes, err := helix.GetUserEmotes(account.Token, account.Id)
	if err != nil {
		log.Printf("Failed to load twitch user emotes: %s", err)
		return
	}

	emoteObjs := []Emote{}
	for _, emote := range *userEmotes {
		emoteObjs = append(emoteObjs, Emote{
			Id:        emote.ID,
			Source:    Twitch,
			Name:      emote.Name,
			Animated:  emote.Animated(),
			Width:     0,
			Height:    0,
			ChannelID: nil,
			OwnerID:   account.Id,
		})
	}

	// the account is the owner of its emotes, so they need a chat user row
	// to reference
	err = hc.memDB.Clauses(clause.OnConflict{DoNothing: true}).Create(&ChatUser{
		Id:          account.Id,
		Login:       account.Login,
		DisplayName: account.DisplayName,
	}).Error
	if err != nil {
		log.Printf("Failed to save the chat user of %s: %s", account.Login, err)
		return
	}

	// the twitch cdn version of the emote is not known, so the image cache
	// revalidates its files with the http validators of the cdn
	if err := hc.replaceEmotes(emoteScope{source: Twitch, ownerID: account.Id}, emoteObjs); err != nil {
		log.Printf("Failed to save twitch user emotes: %s", err)
		return
	}
	hc.userEmotesLoadedId = account.Id
	log.Println("Loaded " + strconv.Itoa(len(emoteObjs)) + " twitch user emotes for " + account.Login)
}

func (hc *HasherinoController) writeSocket() *TwitchChatWebsocket {
	if hc == nil {
		return nil
	}
	hc.socketLock.RLock()
	defer hc.socketLock.RUnlock()
	return hc.writeWS
}

func (hc *HasherinoController) readSocket() *TwitchChatWebsocket {
	if hc == nil {
		return nil
	}
	hc.socketLock.RLock()
	defer hc.socketLock.RUnlock()
	return hc.readWS
}

func (hc *HasherinoController) setWriteSocket(ws *TwitchChatWebsocket) {
	hc.socketLock.Lock()
	defer hc.socketLock.Unlock()
	hc.writeWS = ws
}

func (hc *HasherinoController) setReadSocket(ws *TwitchChatWebsocket) {
	hc.socketLock.Lock()
	defer hc.socketLock.Unlock()
	hc.readWS = ws
}

// ReconnectChat closes the authenticated socket so the next Listen connects
// again with the current account token.
func (hc *HasherinoController) ReconnectChat() {
	if ws := hc.writeSocket(); ws != nil {
		ws.Close()
	}
}

// connectWriteSocket connects the sending socket with the active account's
// token, refreshing the token first when it is about to expire.
func (hc *HasherinoController) connectWriteSocket() error {
	hc.writeConnectLock.Lock()
	defer hc.writeConnectLock.Unlock()

	account, err := hc.GetActiveAccount()
	if err != nil {
		return err
	}
	hc.refreshTokenIfNeeded(account)

	ws, err := (&TwitchChatWebsocket{}).New("oauth:"+account.Token, account.Login)
	if err != nil {
		return err
	}
	if err := ws.Connect(); err != nil {
		return err
	}
	old := hc.writeSocket()
	hc.setWriteSocket(ws)
	if old != nil {
		old.Close()
	}
	ws.StartListening(nil, hc.handleWriteAuthError, nil)
	return nil
}

// refreshTokenIfNeeded refreshes the token when it is expired or about to
// expire. A failure is logged and the current token is kept; reconnecting may
// still work when the expiry metadata is stale.
func (hc *HasherinoController) refreshTokenIfNeeded(account *Account) {
	if account == nil || account.TokenExpiresAt.IsZero() {
		return
	}
	if time.Until(account.TokenExpiresAt) > tokenRefreshMargin {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), tokenRefreshTimeout)
	defer cancel()
	if err := hc.refreshActiveAccountToken(ctx, account); err != nil {
		log.Printf("Failed to refresh the twitch token of %s: %s", account.Login, err)
	}
}

// refreshActiveAccountToken exchanges the account's refresh token for a new
// access token and persists the rotated pair. Device-flow refresh tokens are
// single use, so the new refresh token must be stored.
func (hc *HasherinoController) refreshActiveAccountToken(ctx context.Context, account *Account) error {
	if account == nil {
		return errors.New("no active account")
	}
	hc.authRefreshLock.Lock()
	defer hc.authRefreshLock.Unlock()

	// another refresh or a re-authorization may have rotated the tokens
	// while this one was waiting for the lock; use the newest values
	latest := &Account{}
	if err := hc.permDB.Take(latest, "id = ?", account.Id).Error; err == nil {
		account.Token = latest.Token
		account.RefreshToken = latest.RefreshToken
		account.TokenExpiresAt = latest.TokenExpiresAt
	}

	if account.RefreshToken == "" {
		return errNoRefreshToken
	}
	usedRefreshToken := account.RefreshToken
	token, err := hc.twitchOAuth.RefreshDeviceToken(ctx, hc.appId, usedRefreshToken)
	if err != nil {
		return err
	}
	if token.AccessToken == "" {
		return errors.New("twitch returned an empty access token")
	}
	refreshToken := token.RefreshToken
	if refreshToken == "" {
		refreshToken = account.RefreshToken
	}
	var expiresAt time.Time
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}

	// only store the result when the refresh token is still the one that was
	// exchanged; re-authorizing the account rotates it as well, and must win
	result := hc.permDB.Model(&Account{}).
		Where("id = ? AND refresh_token = ?", account.Id, usedRefreshToken).
		Updates(map[string]interface{}{
			"token":            token.AccessToken,
			"refresh_token":    refreshToken,
			"token_expires_at": expiresAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("the account tokens changed while refreshing")
	}
	account.Token = token.AccessToken
	account.RefreshToken = refreshToken
	account.TokenExpiresAt = expiresAt
	log.Printf("Refreshed the twitch token of %s", account.Login)
	return nil
}

// handleWriteAuthError refreshes the rejected token and reconnects the
// sending socket. It runs in its own goroutine because the listener stops
// after an authentication failure.
func (hc *HasherinoController) handleWriteAuthError() {
	go func() {
		log.Println("Twitch rejected the chat token; refreshing it")
		account, err := hc.GetActiveAccount()
		if err != nil {
			log.Printf("Failed to refresh the chat token: %s", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), tokenRefreshTimeout)
		defer cancel()
		if err := hc.refreshActiveAccountToken(ctx, account); err != nil {
			log.Printf("Failed to refresh the chat token: %s; re-authorize the account", err)
			// stop the failed socket and its watchdog; sending reports that
			// the chat is not connected until the account is re-authorized
			hc.ReconnectChat()
			return
		}
		if err := hc.connectWriteSocket(); err != nil {
			log.Printf("Failed to reconnect chat after refreshing the token: %s", err)
			return
		}
		// the emotes of the account may have failed while the token was
		// expired; retrying is a no-op when they already loaded
		if active, err := hc.GetActiveAccount(); err == nil {
			account := *active
			go hc.loadUserEmotes(&account)
		}
	}()
}

func (hc *HasherinoController) Listen() error {
	hc.listenLock.Lock()
	defer hc.listenLock.Unlock()

	activeAccount, err := hc.GetActiveAccount()
	if err == nil {
		// refresh before the helix calls below, so an expired token does not
		// fail the emote list load
		hc.refreshTokenIfNeeded(activeAccount)
		account := *activeAccount
		go hc.loadUserEmotes(&account)
		// always reconnect: Listen is called when the active account or its
		// token changed, and the socket has to use the current credentials
		if err := hc.connectWriteSocket(); err != nil {
			return err
		}
	}
	if ws := hc.readSocket(); ws == nil || !ws.IsConnected() {
		ws, err := (&TwitchChatWebsocket{}).New("SCHMOOPIIE", "justinfan71097")
		if err != nil {
			return err
		}
		if err := ws.Connect(); err != nil {
			return err
		}
		old := hc.readSocket()
		hc.setReadSocket(ws)
		if old != nil {
			old.Close()
		}
	}

	callbackWrapper := func(message string) {
		msg, err := ParseMessage(message)
		if err != nil {
			log.Printf("Failed to parse message: %s", err)
			return
		}

		callback, ok := hc.callbackMap[msg.Channel]
		if !ok {
			log.Printf("No callback for channel %s.", msg.Channel)
			return
		}
		callback(*msg)
	}

	readWS := hc.readSocket()
	if readWS == nil {
		return errors.New("chat reader is not connected")
	}
	for channel := range hc.callbackMap {
		if !readWS.HasChannel(channel) {
			if err := readWS.Join(channel); err != nil {
				log.Printf("Failed to join channel %s: %s", channel, err)
			}
		}
	}
	readWS.StartListening(callbackWrapper, nil, hc.notifyChatReconnected)
	return nil
}

func (hc *HasherinoController) IsChannelJoined(channel string) bool {
	readWS := hc.readSocket()
	return readWS != nil && readWS.IsConnected() && readWS.HasChannel(channel)
}

func (hc *HasherinoController) SendMessage(channel string, message string) error {
	return hc.SendReply(channel, message, "")
}

// SendReply sends a chat message that replies to the message with replyID.
func (hc *HasherinoController) SendReply(channel string, message string, replyID string) error {
	ws := hc.writeSocket()
	if ws == nil {
		return ErrNotConnected
	}
	if err := ws.SendReply(channel, message, replyID); err != nil {
		// the socket may have been replaced by a reconnect between reading
		// the pointer and writing; retry once on the new connection
		if current := hc.writeSocket(); current != nil && current != ws && current.IsConnected() {
			return current.SendReply(channel, message, replyID)
		}
		return err
	}
	return nil
}

func (hc *HasherinoController) GetSettings() (*AppSettings, error) {
	appSettings := &AppSettings{}
	result := hc.permDB.Take(appSettings)
	return appSettings, result.Error
}

func (hc *HasherinoController) SetSettings(appSettings *AppSettings) error {
	return hc.permDB.Save(appSettings).Error
}

func GetDataFolder() (string, error) {
	dir, err := os.UserConfigDir()
	slog.Info("user data dir", slog.String("dir", dir))
	if err != nil {
		return "", err
	}
	return path.Join(dir, "hasherino"), nil
}

// GetCacheFolder returns the folder for disposable cached data, like the
// downloaded emote files. It lives in the user's cache directory, so the data
// can be deleted at any time and is not part of configuration backups or
// roaming profiles.
func GetCacheFolder() (string, error) {
	if dir := os.Getenv("HASHERINO_CACHE_DIR"); dir != "" {
		return dir, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		// some environments, like minimal CI containers, have no cache
		// directory; fall back to the data folder instead of disabling the
		// cache
		return GetDataFolder()
	}
	return filepath.Join(dir, "hasherino"), nil
}
