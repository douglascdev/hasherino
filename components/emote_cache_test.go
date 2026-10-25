package components

import (
	"bytes"
	"errors"
	"image"
	"image/color/palette"
	"image/gif"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func init() {
	// keep the other tests of this package offline
	downloadEmote = func(*hasherino.Emote) ([]byte, error) {
		return nil, errEmoteDownloadsDisabled
	}
}

// errEmoteDownloadsDisabled is returned by the download stub that keeps the
// tests of this package offline.
var errEmoteDownloadsDisabled = errors.New("emote downloads are disabled in tests")

// disableEmoteDownloads stubs the emote downloads for the duration of the test.
func disableEmoteDownloads(t *testing.T) {
	t.Helper()
	downloadEmote = func(*hasherino.Emote) ([]byte, error) {
		return nil, errEmoteDownloadsDisabled
	}
	t.Cleanup(func() {
		downloadEmote = func(*hasherino.Emote) ([]byte, error) {
			return nil, errEmoteDownloadsDisabled
		}
	})
}

func newTestEmoteCache(t *testing.T) *emoteCache {
	t.Helper()
	disableEmoteDownloads(t)
	return &emoteCache{
		dir:     t.TempDir(),
		loading: make(map[string]*emoteLoad),
		decoded: make(map[string]*decodedEmote),
	}
}

func gifEmote(id string) *hasherino.Emote {
	return &hasherino.Emote{Id: id, Source: hasherino.SevenTV, Name: id, Animated: true}
}

func stillEmote(id string) *hasherino.Emote {
	return &hasherino.Emote{Id: id, Source: hasherino.SevenTV, Name: id, Animated: false}
}

func gifBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), palette.Plan9)
	if err := gif.Encode(&buf, frame, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEmoteCacheDeduplicatesConcurrentLoads(t *testing.T) {
	cache := newTestEmoteCache(t)
	data := gifBytes(t)

	var downloads atomic.Int32
	downloadEmote = func(*hasherino.Emote) ([]byte, error) {
		downloads.Add(1)
		time.Sleep(10 * time.Millisecond)
		return data, nil
	}

	emote := gifEmote("dedup")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Gif(emote); err != nil {
				t.Errorf("expected the concurrent load to succeed: %s", err)
			}
		}()
	}
	wg.Wait()

	if downloads.Load() != 1 {
		t.Errorf("expected 1 download for 20 concurrent loads, got %d", downloads.Load())
	}
}

func TestEmoteCachePersistsFilesOnDisk(t *testing.T) {
	disableEmoteDownloads(t)
	data := gifBytes(t)

	var downloads atomic.Int32
	downloadEmote = func(*hasherino.Emote) ([]byte, error) {
		downloads.Add(1)
		return data, nil
	}

	dir := t.TempDir()
	first := &emoteCache{
		dir:     dir,
		loading: make(map[string]*emoteLoad),
		decoded: make(map[string]*decodedEmote),
	}
	emote := gifEmote("persist")
	if _, err := first.Gif(emote); err != nil {
		t.Fatalf("expected the first load to succeed: %s", err)
	}

	// a fresh cache, like after a restart, loads from disk without downloading
	second := &emoteCache{
		dir:     dir,
		loading: make(map[string]*emoteLoad),
		decoded: make(map[string]*decodedEmote),
	}
	if _, err := second.Gif(emote); err != nil {
		t.Fatalf("expected the second load to succeed: %s", err)
	}
	if downloads.Load() != 1 {
		t.Errorf("expected the second load to read from disk, got %d downloads", downloads.Load())
	}
}

func TestEmoteCacheRetriesFailedLoads(t *testing.T) {
	cache := newTestEmoteCache(t)

	var fail = true
	downloadEmote = func(*hasherino.Emote) ([]byte, error) {
		if fail {
			return nil, errors.New("cdn is down")
		}
		return gifBytes(t), nil
	}

	emote := gifEmote("retry")
	if _, err := cache.Gif(emote); err == nil {
		t.Fatal("expected the failed load to return an error")
	}
	fail = false
	if _, err := cache.Gif(emote); err != nil {
		t.Fatalf("expected the load to be retried: %s", err)
	}
}

func TestEmoteCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newTestEmoteCache(t)
	downloadEmote = func(*hasherino.Emote) ([]byte, error) {
		return gifBytes(t), nil
	}

	oldLimit := emoteMemoryLimit
	emoteMemoryLimit = 1
	defer func() { emoteMemoryLimit = oldLimit }()

	first, second := gifEmote("evict-first"), gifEmote("evict-second")
	if _, err := cache.Gif(first); err != nil {
		t.Fatalf("expected the first load to succeed: %s", err)
	}
	if _, err := cache.Gif(second); err != nil {
		t.Fatalf("expected the second load to succeed: %s", err)
	}

	cache.mu.Lock()
	_, hasFirst := cache.decoded[emoteCacheKey(first)]
	_, hasSecond := cache.decoded[emoteCacheKey(second)]
	cache.mu.Unlock()

	if hasFirst {
		t.Error("expected the least recently used emote to be evicted")
	}
	if !hasSecond {
		t.Error("expected the most recent emote to be kept")
	}
}

func TestEmoteCacheReturnsErrorForUndecodableFiles(t *testing.T) {
	cache := newTestEmoteCache(t)
	downloadEmote = func(*hasherino.Emote) ([]byte, error) {
		return []byte("not an image"), nil
	}

	if _, err := cache.Image(stillEmote("broken")); err == nil {
		t.Error("expected the broken still emote to fail")
	}
	if _, err := cache.Gif(gifEmote("broken")); err == nil {
		t.Error("expected the broken animated emote to fail")
	}
}
