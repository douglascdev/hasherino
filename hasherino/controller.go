package hasherino

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

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
	readWS      *TwitchChatWebsocket
	writeWS     *TwitchChatWebsocket
	memDB       *gorm.DB
	permDB      *gorm.DB
	// userEmotesLock and userEmotesLoadedId guard that the twitch emotes of
	// an account are only loaded once per run
	userEmotesLock     sync.Mutex
	userEmotesLoadedId string
}

func (hc *HasherinoController) New(callbackMap map[string]func(ChatMessage)) (*HasherinoController, error) {
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
	permDB.AutoMigrate(&Account{}, &Tab{}, &AppSettings{})

	c := &HasherinoController{
		appId:       "hvmj7blkwy2gw3xf820n47i85g4sub",
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
			ChatHistory:      false,
			ShowMessageTime:  DefaultShowMessageTime,
			EmoteSize:        DefaultEmoteSize,
			FontSize:         DefaultFontSize,
		})
	} else if result.Error != nil {
		return nil, err
	}
	// settings created before the emote and font size settings existed have
	// them set to zero, so they need to be filled with the defaults
	missingDefaults := settings.EmoteSize == 0 || settings.FontSize == 0
	if settings.EmoteSize == 0 {
		settings.EmoteSize = DefaultEmoteSize
	}
	if settings.FontSize == 0 {
		settings.FontSize = DefaultFontSize
	}
	if missingDefaults {
		settings.ShowMessageTime = DefaultShowMessageTime

		if err := permDB.Save(settings).Error; err != nil {
			log.Println("Failed to save default settings:", err)
		}
	}
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

func (hc *HasherinoController) RemoveAccount(id string) error {
	return hc.permDB.Transaction(func(tx *gorm.DB) error {
		account := &Account{}
		result := tx.Take(&account, "Id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if account.Active {
			hc.writeWS.Close()
		}
		result = tx.Delete(&account)
		if result.Error != nil {
			return result.Error
		}
		return nil
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

	hc.writeWS.Close()
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

	err := hc.permDB.Transaction(func(tx *gorm.DB) error {
		_, exists := hc.readWS.channels[channel]
		if exists {
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
			return errors.New("failed to obtain channel's id and login")
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

		err = hc.readWS.Join(login)
		if err != nil {
			log.Printf("failed to join channel %s: %s", login, err)
			return errors.New("failed to join channel " + login)
		}

		err = hc.AddTempTabs(&[]string{users.Data[0].ID})
		if err != nil {
			return err
		}

		return nil
	})
	return err
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
		emoteObjs := []Emote{}

		globalEmotes, err, isCached := STVGetGlobalEmotes()
		if !isCached {
			if err != nil {
				log.Printf("Failed to load global 7tv emotes: %s", err)
			} else {
				for _, emote := range globalEmotes.Data.EmoteSet.Emotes {
					width, height := emote.Data.Dimensions()
					emoteObjs = append(emoteObjs, Emote{
						Id:        emote.ID,
						Source:    SevenTV,
						Name:      emote.Name,
						Animated:  emote.Data.Animated,
						Width:     width,
						Height:    height,
						ChannelID: nil,
						OwnerID:   "",
						Owner:     nil,
					})
				}
				log.Println("Loaded " + strconv.Itoa(len(emoteObjs)) + " 7tv global emotes")
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

			channelEmotes := []Emote{}
			if len(stvUser.Data.UserByConnection.EmoteSets) > 0 {
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
						ChannelID: &channelId,
						OwnerID:   "",
						Owner:     nil,
					})
				}
			}
			log.Println("Loaded " + strconv.Itoa(len(channelEmotes)) + " 7tv user emotes for " + channelId)
			emoteObjs = append(emoteObjs, channelEmotes...)
		}

		if len(emoteObjs) == 0 {
			return
		}
		err = hc.memDB.Transaction(func(tx *gorm.DB) error {
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&emoteObjs).Error
		})
		if err != nil {
			log.Printf("Failed to save 7tv emotes: %s", err)
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

		err := hc.readWS.Part(tab.Login)
		if err != nil {
			return errors.New("failed to part channel" + tab.Login)
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

	err = hc.memDB.Transaction(func(tx *gorm.DB) error {
		// the account is the owner of its emotes, so they need a chat user
		// row to reference
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ChatUser{
			Id:          account.Id,
			Login:       account.Login,
			DisplayName: account.DisplayName,
		})
		if result.Error != nil {
			return result.Error
		}
		if len(emoteObjs) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&emoteObjs).Error
	})
	if err != nil {
		log.Printf("Failed to save twitch user emotes: %s", err)
		return
	}
	hc.userEmotesLoadedId = account.Id
	log.Println("Loaded " + strconv.Itoa(len(emoteObjs)) + " twitch user emotes for " + account.Login)
}

func (hc *HasherinoController) Listen() error {
	activeAccount, err := hc.GetActiveAccount()
	if err == nil {
		go hc.loadUserEmotes(activeAccount)
		if hc.writeWS == nil || hc.writeWS.State == Disconnected {
			hc.writeWS, err = hc.writeWS.New("oauth:"+activeAccount.Token, activeAccount.Login)
			if err != nil {
				return err
			}
			err = hc.writeWS.Connect()
			if err != nil {
				return err
			}
		}
	}
	if hc.readWS == nil || hc.readWS.State == Disconnected {
		hc.readWS, err = hc.readWS.New("SCHMOOPIIE", "justinfan71097")
		if err != nil {
			return err
		}
		err = hc.readWS.Connect()
		if err != nil {
			return err
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

	for channel := range hc.callbackMap {
		_, joinedChannel := hc.writeWS.channels[channel]
		if !joinedChannel {
			hc.readWS.Join(channel)
		}
	}

	go hc.readWS.Listen(callbackWrapper)
	return nil
}

func (hc *HasherinoController) IsChannelJoined(channel string) bool {
	if hc.readWS == nil || hc.readWS.State == Disconnected {
		return false
	}

	_, joinedChannel := hc.readWS.channels[channel]
	return joinedChannel
}

func (hc *HasherinoController) SendMessage(channel string, message string) error {
	err := hc.writeWS.Send(channel, message)
	return err
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
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Local", "Hasherino"), nil
	case "linux":
		return filepath.Join(home, ".local", "share", "hasherino"), nil
	default:
		return home, nil
	}
}
