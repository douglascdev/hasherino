package hasherino

import (
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// --- permDB models ---
type Account struct {
	Id          string `gorm:"primaryKey"`
	Login       string
	DisplayName string
	Active      bool
	Token       string // TODO: enable db encryption to hide token
	// RefreshToken is the device-flow refresh token. Twitch rotates it on
	// every refresh, so the newest value has to be stored.
	RefreshToken string
	// TokenExpiresAt is when Token expires, zero when unknown.
	TokenExpiresAt time.Time
}

type Tab struct {
	Id          string `gorm:"primaryKey"`
	Login       string
	DisplayName string
	Selected    bool
}

// Single row table for global settings
type AppSettings struct {
	gorm.Model
	ChatMessageLimit int // Maximum amount of messages in a single chat
	ChatHistory      bool
	ShowMessageTime  bool
	EmoteSize        int // Size chat emotes are rendered at, in pixels
	FontSize         int // Size chat text is rendered at, in pixels
	SettingsVersion  int // Version of the defaults last applied to these settings
}

const (
	DefaultEmoteSize       = 36
	DefaultFontSize        = 18
	DefaultShowMessageTime = true
	DefaultChatHistory     = true

	// currentSettingsVersion is bumped when a default has to be applied to
	// existing installations exactly once.
	currentSettingsVersion = 1
)

// --- tempDB models ---
type EmoteSourceEnum int64

const (
	Twitch EmoteSourceEnum = iota
	SevenTV
)

// Handles tab data that is not persisted
type TempTab struct {
	Id        string     `gorm:"primaryKey"`
	ChatUsers []ChatUser `gorm:"many2many:chat_user_temp_tab;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
}

type ChatUser struct {
	Id          string `gorm:"primaryKey"`
	Login       string
	DisplayName string
	TempTabs    []TempTab `gorm:"many2many:chat_user_temp_tab;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Emotes      []Emote   `gorm:"foreignKey:OwnerID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
}

type ChatUserTempTab struct {
	ChatUserId string `gorm:"primaryKey"`
	TempTabId  string `gorm:"primaryKey"`
}

// When a TempTab gets deleted, delete all orphaned ChatUsers
func (c *ChatUserTempTab) AfterDelete(tx *gorm.DB) (err error) {
	var count int64
	if err := tx.Model(&ChatUserTempTab{}).Where("ChatUserId = ?", c.ChatUserId).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		if err := tx.Delete(&ChatUser{Id: c.ChatUserId}).Error; err != nil {
			return err
		}
	}
	return nil
}

type Emote struct {
	Id     string          `gorm:"primaryKey"`
	Source EmoteSourceEnum `gorm:"primaryKey"`
	Name   string          `gorm:"primaryKey"` // aliases of one emote are separate rows

	Animated  bool
	Width     int       // intrinsic width of the emote from its CDN, 0 when unknown
	Height    int       // intrinsic height of the emote from its CDN, 0 when unknown
	Version   string    // identifies the content of the emote's file, empty when unknown
	ChannelID *string   `gorm:"primaryKey"`                       // if a channel is set, only renders in that channel
	OwnerID   string    `gorm:"primaryKey;index"`                 // Foreign key field
	Owner     *ChatUser `gorm:"foreignKey:OwnerID;references:Id"` // if an owner is set, only renders when the message sender is the owner

	// DedupeKey is a non-null identity for the emote. The composite primary
	// key contains a nullable ChannelID, and SQLite treats NULLs as distinct,
	// so without it cached emotes could be hydrated more than once.
	DedupeKey string `gorm:"uniqueIndex"`
}

// BeforeCreate fills the DedupeKey of emotes that were not built by the
// controller, like the ones created by tests.
func (e *Emote) BeforeCreate(tx *gorm.DB) error {
	if e.DedupeKey == "" {
		e.DedupeKey = e.identity()
	}
	return nil
}

// identity returns the key that uniquely identifies the emote within the
// in-memory database. The name is part of it because 7tv sets can contain
// several aliases of the same emote.
func (e *Emote) identity() string {
	return emoteKey(e.Source, e.Id, e.Name, e.ChannelID, e.OwnerID)
}

// CachedEmote is the persisted emote catalog. It mirrors Emote so that a
// chat can render its emotes immediately on startup, before the emote lists
// are fetched again, and so that changed emotes can be detected when they
// are.
type CachedEmote struct {
	Key string `gorm:"primaryKey"`
	Id  string `gorm:"index"`

	Source    EmoteSourceEnum
	Name      string
	Animated  bool
	Width     int
	Height    int
	Version   string
	ChannelID *string `gorm:"index"`
	OwnerID   string  `gorm:"index"`
}

// BeforeCreate fills the key of cached emotes created directly, so a cached
// emote has the same identity as its in-memory counterpart.
func (e *CachedEmote) BeforeCreate(tx *gorm.DB) error {
	if e.Key == "" {
		e.Key = emoteKey(e.Source, e.Id, e.Name, e.ChannelID, e.OwnerID)
	}
	return nil
}

func emoteKey(source EmoteSourceEnum, id string, name string, channelID *string, ownerID string) string {
	channel := ""
	if channelID != nil {
		channel = *channelID
	}
	return strconv.FormatInt(int64(source), 10) + ":" + id + ":" + name + ":" + channel + ":" + ownerID
}

// emote returns the in-memory emote of the cached catalog entry.
func (e *CachedEmote) emote() Emote {
	return Emote{
		Id:        e.Id,
		Source:    e.Source,
		Name:      e.Name,
		Animated:  e.Animated,
		Width:     e.Width,
		Height:    e.Height,
		Version:   e.Version,
		ChannelID: e.ChannelID,
		OwnerID:   e.OwnerID,
		DedupeKey: e.Key,
	}
}

// cachedEmote returns the catalog entry of the in-memory emote.
func cachedEmote(e *Emote) CachedEmote {
	return CachedEmote{
		Key:       emoteKey(e.Source, e.Id, e.Name, e.ChannelID, e.OwnerID),
		Id:        e.Id,
		Source:    e.Source,
		Name:      e.Name,
		Animated:  e.Animated,
		Width:     e.Width,
		Height:    e.Height,
		Version:   e.Version,
		ChannelID: e.ChannelID,
		OwnerID:   e.OwnerID,
	}
}

func (e *Emote) GetUrl() (string, error) {
	var result string
	switch e.Source {
	case Twitch:
		result = "https://static-cdn.jtvnw.net/emoticons/v2/" + e.Id + "/default/dark/2.0"
	case SevenTV:
		result = "https://cdn.7tv.app/emote/" + e.Id + "/2x"
	default:
		return "", errors.New("Unknown emote source")
	}
	return result + e.GetUrlExtension(), nil
}

func (e *Emote) GetUrlExtension() string {
	switch e.Source {
	case Twitch:
		return ""
	case SevenTV:
		if e.Animated {
			return ".gif"
		}
		return ".webp"
	default:
		return ".webp"
	}
}
