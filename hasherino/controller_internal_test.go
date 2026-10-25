package hasherino

import (
	"slices"
	"strconv"
	"sync"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMemDBConcurrentReadWrite(t *testing.T) {
	memDB, err := openMemDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := memDB.AutoMigrate(&TempTab{}, &ChatUser{}, &Emote{}, &ChatUserTempTab{}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			emotes := []Emote{{Id: strconv.Itoa(i), Source: SevenTV, Name: "emote" + strconv.Itoa(i)}}
			if err := memDB.Create(&emotes).Error; err != nil {
				t.Errorf("concurrent write failed: %s", err)
			}
		}(i)
		go func() {
			defer wg.Done()
			emotes := []*Emote{}
			if err := memDB.Find(&emotes).Error; err != nil {
				t.Errorf("concurrent read failed: %s", err)
			}
		}()
	}
	wg.Wait()
}

func newSearchEmoteController(t *testing.T) *HasherinoController {
	t.Helper()

	newDB := func(models ...interface{}) *gorm.DB {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		if err := db.AutoMigrate(models...); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = sqlDB.Close()
		})
		return db
	}

	return &HasherinoController{
		memDB:  newDB(&Emote{}),
		permDB: newDB(&Account{}, &Tab{}),
	}
}

func TestSearchEmotesRanksFiltersAndLimits(t *testing.T) {
	hc := newSearchEmoteController(t)
	if err := hc.permDB.Create(&Account{Id: "account", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := hc.permDB.Create(&Tab{Id: "tab", Login: "channel", Selected: true}).Error; err != nil {
		t.Fatal(err)
	}

	otherChannel := "other-channel"
	emotes := []Emote{
		{Id: "1", Source: SevenTV, Name: "Pog"},
		{Id: "2", Source: SevenTV, Name: "PogChamp"},
		{Id: "3", Source: SevenTV, Name: "pogU"},
		{Id: "4", Source: SevenTV, Name: "SuperPog"},
		{Id: "5", Source: SevenTV, Name: "PogOwned", OwnerID: "someone-else"},
		{Id: "6", Source: SevenTV, Name: "PogElsewhere", ChannelID: &otherChannel},
	}
	if err := hc.memDB.Create(&emotes).Error; err != nil {
		t.Fatal(err)
	}

	names := func(emotes []*Emote) []string {
		result := make([]string, 0, len(emotes))
		for _, emote := range emotes {
			result = append(result, emote.Name)
		}
		return result
	}

	limited, err := hc.SearchEmotes("pog", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(limited), []string{"Pog", "PogChamp", "pogU"}; !slices.Equal(got, want) {
		t.Fatalf("unexpected ranked results: got %v, want %v", got, want)
	}

	all, err := hc.SearchEmotes("pog", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(all), []string{"Pog", "PogChamp", "pogU", "SuperPog"}; !slices.Equal(got, want) {
		t.Fatalf("unexpected all results: got %v, want %v", got, want)
	}
}

func TestSearchEmotesEscapesLikeWildcards(t *testing.T) {
	hc := newSearchEmoteController(t)
	if err := hc.permDB.Create(&Account{Id: "account", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := hc.permDB.Create(&Tab{Id: "tab", Login: "channel", Selected: true}).Error; err != nil {
		t.Fatal(err)
	}
	emotes := []Emote{
		{Id: "1", Source: SevenTV, Name: "100%"},
		{Id: "2", Source: SevenTV, Name: "under_score"},
		{Id: "3", Source: SevenTV, Name: "plain"},
	}
	if err := hc.memDB.Create(&emotes).Error; err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		search string
		name   string
	}{
		{search: "%", name: "100%"},
		{search: "_", name: "under_score"},
	} {
		results, err := hc.SearchEmotes(test.search, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 1 || results[0].Name != test.name {
			t.Fatalf("expected only %q for %q, got %v", test.name, test.search, results)
		}
	}
}
