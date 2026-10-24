package hasherino

import (
	"strconv"
	"sync"
	"testing"
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
