package components

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Hashy-Software/hasherino-go/hasherino"
	"golang.org/x/image/webp"
)

const (
	// emoteCacheFolder is the folder inside the app data folder that stores
	// downloaded emote files, so emotes are only ever downloaded once.
	emoteCacheFolder = "emote-cache"
	// emoteCacheMaxAge is how long unused emote files are kept on disk
	// before they are removed again.
	emoteCacheMaxAge = 30 * 24 * time.Hour
	// emoteDownloadTimeout limits how long a single emote download may take.
	emoteDownloadTimeout = 20 * time.Second
	// emoteMaxParallelDownloads limits how many emotes are downloaded at
	// the same time, so bursts of many distinct emotes don't saturate the
	// connection.
	emoteMaxParallelDownloads = 8
)

// emoteMemoryLimit is how many bytes of decoded emotes are kept in memory.
// Decoded emotes are shared by every widget showing them, and the least
// recently used ones are evicted once the limit is exceeded. It is a
// variable so that tests can lower it.
var emoteMemoryLimit int64 = 128 << 20

var (
	emoteHTTPClient     = &http.Client{Timeout: emoteDownloadTimeout}
	emoteDownloadSlots  = make(chan struct{}, emoteMaxParallelDownloads)
	emoteCacheOnce      sync.Once
	emoteCacheSingleton *emoteCache
)

// downloadEmote downloads the file of the emote from its CDN. It is a
// variable so that tests can stub the network access.
var downloadEmote = func(emote *hasherino.Emote) ([]byte, error) {
	url, err := emote.GetUrl()
	if err != nil {
		return nil, err
	}

	emoteDownloadSlots <- struct{}{}
	defer func() { <-emoteDownloadSlots }()

	resp, err := emoteHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading emote %s failed: %s", emote.Name, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// emoteCache stores downloaded emote files on disk and decoded emotes in
// memory. Loading the same emote concurrently or repeatedly downloads and
// decodes it only once, and the decoded emote is shared by every widget
// showing it.
type emoteCache struct {
	// dir is the folder the emote files are cached in. An empty dir
	// disables the disk cache and only caches in memory.
	dir string

	mu      sync.Mutex
	loading map[string]*emoteLoad
	decoded map[string]*decodedEmote
	used    int64
}

// emoteLoad is an in-flight emote load that concurrent callers of the same
// emote wait for.
type emoteLoad struct {
	done      chan struct{}
	image     image.Image
	animation *gifAnimation
	err       error
}

// decodedEmote is a decoded emote kept in memory.
type decodedEmote struct {
	image     image.Image
	animation *gifAnimation
	bytes     int64
	lastUsed  time.Time
}

// getEmoteCache returns the emote cache shared by all emote widgets.
func getEmoteCache() *emoteCache {
	emoteCacheOnce.Do(func() {
		dir, err := emoteCacheDir()
		if err != nil {
			log.Printf("Failed to locate the emote cache folder: %s", err)
			dir = ""
		} else if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("Failed to create the emote cache folder: %s", err)
			dir = ""
		} else {
			go cleanEmoteCache(dir)
		}
		emoteCacheSingleton = &emoteCache{
			dir:     dir,
			loading: make(map[string]*emoteLoad),
			decoded: make(map[string]*decodedEmote),
		}
	})
	return emoteCacheSingleton
}

func emoteCacheDir() (string, error) {
	dataFolder, err := hasherino.GetDataFolder()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataFolder, emoteCacheFolder), nil
}

// cleanEmoteCache removes emote files that have not been used for a long time.
func cleanEmoteCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	stale := time.Now().Add(-emoteCacheMaxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(stale) {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// Image returns the decoded still image of the emote, downloading and caching
// it if necessary. For animated emotes it is the first frame of the
// animation.
func (c *emoteCache) Image(emote *hasherino.Emote) (image.Image, error) {
	img, _, err := c.load(emote)
	if err != nil {
		return nil, err
	}
	if img == nil {
		return nil, fmt.Errorf("emote %s did not decode to a still image", emote.Name)
	}
	return img, nil
}

// Gif returns the animation of the emote with every frame pre-composited,
// downloading and caching it if necessary.
func (c *emoteCache) Gif(emote *hasherino.Emote) (*gifAnimation, error) {
	_, animation, err := c.load(emote)
	if err != nil {
		return nil, err
	}
	if animation == nil {
		return nil, fmt.Errorf("emote %s did not decode to an animated gif", emote.Name)
	}
	return animation, nil
}

// load returns the decoded emote, downloading and caching it if necessary.
// Concurrent loads of the same emote are deduplicated into one download and
// decode. Failed loads are not cached, so they are retried on the next call.
func (c *emoteCache) load(emote *hasherino.Emote) (image.Image, *gifAnimation, error) {
	key := emoteCacheKey(emote)

	c.mu.Lock()
	if decoded, ok := c.decoded[key]; ok {
		decoded.lastUsed = time.Now()
		c.mu.Unlock()
		return decoded.image, decoded.animation, nil
	}
	if load, ok := c.loading[key]; ok {
		c.mu.Unlock()
		<-load.done
		return load.image, load.animation, load.err
	}
	load := &emoteLoad{done: make(chan struct{})}
	c.loading[key] = load
	c.mu.Unlock()

	img, animation, err := c.fetchAndDecode(emote)

	load.image, load.animation, load.err = img, animation, err
	close(load.done)

	c.mu.Lock()
	delete(c.loading, key)
	if err == nil {
		entry := &decodedEmote{image: img, animation: animation, bytes: decodedSize(img, animation), lastUsed: time.Now()}
		c.decoded[key] = entry
		c.used += entry.bytes
		c.evictLocked()
	}
	c.mu.Unlock()

	return img, animation, err
}

// fetchAndDecode fetches the file of the emote and decodes it by the format
// of the file instead of the animated flag, because the animated flag is not
// known for the emotes of irc tags: the twitch cdn serves png files for
// static and gif files for animated emotes, 7tv serves webp and gif. Gif
// files decode into their animation and the still image of their first
// frame.
func (c *emoteCache) fetchAndDecode(emote *hasherino.Emote) (image.Image, *gifAnimation, error) {
	data, fromDisk, err := c.emoteFile(emote)
	if err != nil {
		return nil, nil, err
	}

	var img image.Image
	var animation *gifAnimation
	switch {
	case isGif(data):
		var src *gif.GIF
		src, err = gif.DecodeAll(bytes.NewReader(data))
		if err == nil {
			animation = buildAnimation(src)
			if animation == nil {
				err = errors.New("gif has no frames")
			} else {
				img = animation.frames[0]
			}
		}
	case isPng(data):
		img, err = png.Decode(bytes.NewReader(data))
	default:
		img, err = webp.Decode(bytes.NewReader(data))
	}
	if err != nil {
		if fromDisk {
			// the cached file is unreadable, remove it so the next load
			// downloads a fresh copy
			os.Remove(c.path(emote))
		}
		return nil, nil, err
	}
	return img, animation, nil
}

// isGif reports whether the data starts with the magic bytes of a gif.
func isGif(data []byte) bool {
	return len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
}

// isPng reports whether the data starts with the magic bytes of a png.
func isPng(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
}

// emoteFile returns the raw file of the emote and whether it was read from the
// disk cache. Files missing from disk are downloaded and stored there.
func (c *emoteCache) emoteFile(emote *hasherino.Emote) ([]byte, bool, error) {
	if c.dir != "" {
		if data, err := os.ReadFile(c.path(emote)); err == nil && len(data) > 0 {
			return data, true, nil
		}
	}

	data, err := downloadEmote(emote)
	if err != nil {
		return nil, false, err
	}

	if c.dir != "" {
		if err := writeEmoteFile(c.path(emote), data); err != nil {
			log.Printf("Failed to cache emote %s: %s", emote.Name, err)
		}
	}
	return data, false, nil
}

func (c *emoteCache) path(emote *hasherino.Emote) string {
	return filepath.Join(c.dir, emoteCacheKey(emote))
}

// emoteCacheKey identifies the file of the emote on disk.
func emoteCacheKey(emote *hasherino.Emote) string {
	extension := emote.GetUrlExtension()
	if extension == "" {
		extension = ".img"
	}
	return strconv.FormatInt(int64(emote.Source), 10) + "-" + emote.Id + extension
}

// writeEmoteFile writes the file atomically, so that concurrent readers never
// see a partially written file.
func writeEmoteFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// evictLocked removes the least recently used decoded emotes until the
// memory limit is honored. It keeps at least the most recent emote.
func (c *emoteCache) evictLocked() {
	for c.used > emoteMemoryLimit && len(c.decoded) > 1 {
		oldestKey := ""
		var oldest time.Time
		for key, decoded := range c.decoded {
			if oldestKey == "" || decoded.lastUsed.Before(oldest) {
				oldestKey, oldest = key, decoded.lastUsed
			}
		}
		if oldestKey == "" {
			return
		}
		c.used -= c.decoded[oldestKey].bytes
		delete(c.decoded, oldestKey)
	}
}

// decodedSize estimates how many bytes of memory the decoded emote occupies.
func decodedSize(img image.Image, animation *gifAnimation) int64 {
	if animation != nil {
		// the frames hold the still image of the animation as well
		var size int64
		for _, frame := range animation.frames {
			bounds := frame.Bounds()
			size += int64(bounds.Dx()) * int64(bounds.Dy()) * 4
		}
		return size
	}
	if img != nil {
		bounds := img.Bounds()
		return int64(bounds.Dx()) * int64(bounds.Dy()) * 4
	}
	return 0
}
