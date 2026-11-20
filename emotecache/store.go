// Package emotecache keeps downloaded emote files in the user's cache
// directory. It serves cached files immediately, revalidates them in the
// background with the http validators of the cdn, and removes files that
// have not been used for a long time or that no longer fit in the cache.
package emotecache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

const (
	// folderName is the folder inside the app cache folder that stores the
	// downloaded emote files.
	folderName = "emote-cache"
	// indexName is the file that stores the http validators and timestamps
	// of the cached emote files.
	indexName = "index.json"
	// indexVersion is bumped when the format of indexName changes.
	indexVersion = 1
	// downloadTimeout limits how long a single emote download may take.
	downloadTimeout = 20 * time.Second
	// maxParallelDownloads limits how many emotes are downloaded at the same
	// time, so bursts of many distinct emotes don't saturate the connection.
	maxParallelDownloads = 8
	// touchInterval is how often reading a cached file refreshes its
	// modification time, which is what the cleanup uses as last use.
	touchInterval = time.Hour
	// maxVersionLength keeps the version part of a cache key short.
	maxVersionLength = 32
)

var (
	// CacheMaxAge is how long unused emote files are kept on disk.
	CacheMaxAge = 30 * 24 * time.Hour
	// CacheMaxBytes is how many bytes of emote files are kept on disk. The
	// least recently used files are removed once it is exceeded.
	CacheMaxBytes int64 = 512 << 20
	// RevalidateAfter is how long a cached file is served before it is
	// revalidated with its cdn in the background.
	RevalidateAfter = 7 * 24 * time.Hour
)

// errNotModified reports that the cdn confirmed the cached file is current.
var errNotModified = errors.New("emote file is not modified")

// emoteURL returns the cdn url of an emote. It is a variable so that tests
// can serve the files themselves.
var emoteURL = func(emote *hasherino.Emote) (string, error) {
	return emote.GetUrl()
}

// entry is the metadata of one cached emote file.
type entry struct {
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	FetchedAt    time.Time `json:"fetchedAt"`
	Size         int64     `json:"size"`
}

type indexDocument struct {
	Version int               `json:"version"`
	Entries map[string]*entry `json:"entries"`
}

// Store caches the files of emotes on disk. It is safe for concurrent use.
// A store without a directory only downloads emotes without caching them,
// which is used when no cache directory is available.
type Store struct {
	dir    string
	client *http.Client
	now    func() time.Time
	slots  chan struct{}

	// the limits are copied from the package defaults when the store is
	// created and can be changed with SetLimits
	maxAge          time.Duration
	maxBytes        int64
	revalidateAfter time.Duration

	mu           sync.Mutex
	entries      map[string]*entry
	revalidating map[string]struct{}
	onUpdate     func(*hasherino.Emote)

	// indexMu serializes writes of the index, so an older snapshot cannot
	// overwrite a newer one. Concurrent requests are coalesced into a single
	// write, because refreshing a large emote list updates many entries.
	indexMu      sync.Mutex
	indexWriting bool
	indexPending bool
}

// Open opens the store in dir, creating it when needed, and starts the
// background cleanup of stale files. An empty dir disables the disk cache.
func Open(dir string) (*Store, error) {
	store := NewMemory()
	if dir == "" {
		return store, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	store.dir = dir
	store.loadIndex()
	store.removeTemporaryFiles()
	go store.Clean()
	return store, nil
}

// OpenDefault opens the store in the default cache folder.
func OpenDefault() (*Store, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return Open(dir)
}

// NewMemory returns a store that downloads emotes without keeping them.
func NewMemory() *Store {
	return &Store{
		client:          &http.Client{Timeout: downloadTimeout},
		now:             time.Now,
		slots:           make(chan struct{}, maxParallelDownloads),
		maxAge:          CacheMaxAge,
		maxBytes:        CacheMaxBytes,
		revalidateAfter: RevalidateAfter,
		entries:         make(map[string]*entry),
		revalidating:    make(map[string]struct{}),
	}
}

// DefaultDir returns the folder the emote files are cached in.
func DefaultDir() (string, error) {
	base, err := hasherino.GetCacheFolder()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, folderName), nil
}

// SetOnUpdate sets the function called when revalidating an emote found new
// content, so the caller can drop its decoded copy and repaint.
func (s *Store) SetOnUpdate(update func(*hasherino.Emote)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onUpdate = update
}

// SetLimits changes how long cached files are kept, how many bytes are kept
// and how long a file is served before it is revalidated. It is safe to call
// while the store is in use.
func (s *Store) SetLimits(maxAge time.Duration, maxBytes int64, revalidateAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxAge = maxAge
	s.maxBytes = maxBytes
	s.revalidateAfter = revalidateAfter
}

// SetClient replaces the http client of the store. It is meant for tests.
func (s *Store) SetClient(client *http.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = client
}

// Key identifies the file of the emote in the cache. It contains the source,
// the id and, when known, the content version of the emote, so a new version
// of an emote never reads the file of the old one.
func Key(emote *hasherino.Emote) string {
	if emote == nil {
		return ""
	}
	extension := emote.GetUrlExtension()
	if extension == "" {
		extension = ".img"
	}
	key := strconv.FormatInt(int64(emote.Source), 10) + "-" + sanitize(emote.Id)
	if version := sanitize(emote.Version); version != "" {
		if len(version) > maxVersionLength {
			version = version[:maxVersionLength]
		}
		key += "-" + version
	}
	if resolution := sanitize(emote.Resolution); resolution != "" {
		key += "-" + resolution
	}
	return key + extension
}

// sanitize keeps only characters that are safe in a file name, so an emote
// id or version can never escape the cache folder.
func sanitize(value string) string {
	var result strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// Cached reports whether the file of the emote is already on disk.
func (s *Store) Cached(emote *hasherino.Emote) bool {
	if emote == nil || s.dir == "" {
		return false
	}
	info, err := os.Stat(s.path(emote))
	return err == nil && info.Size() > 0
}

// Load returns the file of the emote, from disk when possible and from its
// cdn otherwise. The cached flag reports whether the data came from disk.
// Cached files are returned immediately; when they are old they are
// revalidated in the background.
func (s *Store) Load(emote *hasherino.Emote) (data []byte, cached bool, err error) {
	if emote == nil {
		return nil, false, errors.New("cannot load a nil emote")
	}
	key := Key(emote)
	if s.dir != "" {
		if data, err := os.ReadFile(filepath.Join(s.dir, key)); err == nil && len(data) > 0 {
			s.touch(key)
			s.afterRead(emote, key)
			return data, true, nil
		}
	}

	data, meta, err := s.download(emote, nil)
	if err != nil {
		return nil, false, err
	}
	s.storeBytes(emote, data, meta)
	return data, false, nil
}

// Revalidate refreshes a cached file in the background when it is older than
// RevalidateAfter. It is meant to be called when a refreshed emote list is
// loaded, so changed emotes are picked up without blocking the ui.
func (s *Store) Revalidate(emote *hasherino.Emote) {
	if emote == nil || s.dir == "" || !s.Cached(emote) {
		return
	}
	key := Key(emote)
	s.mu.Lock()
	cached, ok := s.entries[key]
	stale := ok && s.now().Sub(cached.FetchedAt) >= s.revalidateAfter
	s.mu.Unlock()
	if !stale {
		return
	}
	s.scheduleRevalidate(emote)
}

// Invalidate removes the cached file of the emote and its metadata, so the
// next load downloads a fresh copy. It is used when the emote's list reports
// changed content.
func (s *Store) Invalidate(emote *hasherino.Emote) {
	s.InvalidateAll([]*hasherino.Emote{emote})
}

// InvalidateAll invalidates many emotes at once. It writes the index once for
// the whole batch, so refreshing a large emote list stays cheap.
func (s *Store) InvalidateAll(emotes []*hasherino.Emote) {
	changed := false
	for _, emote := range emotes {
		if emote == nil {
			continue
		}
		key := Key(emote)
		if key == "" {
			continue
		}
		if s.dir != "" {
			if err := os.Remove(filepath.Join(s.dir, key)); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("Failed to remove cached emote %s: %s", key, err)
			}
		}
		s.mu.Lock()
		_, cached := s.entries[key]
		delete(s.entries, key)
		s.mu.Unlock()
		changed = changed || cached
	}
	if changed {
		s.writeIndex()
	}
}

// Clean removes files that have not been used for the store's maximum age
// and the least recently used files above its maximum size. It runs in the
// background when the store is opened.
func (s *Store) Clean() {
	if s.dir == "" {
		return
	}
	s.mu.Lock()
	maxAge, maxBytes := s.maxAge, s.maxBytes
	s.mu.Unlock()
	dirEntries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}

	type cachedFile struct {
		name string
		size int64
		mod  time.Time
	}
	files := make([]cachedFile, 0, len(dirEntries))
	removed := false
	now := s.now()
	var total int64
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() {
			continue
		}
		name := dirEntry.Name()
		if name == indexName {
			continue
		}
		path := filepath.Join(s.dir, name)
		if strings.HasPrefix(name, "tmp-") {
			// only remove the leftovers of interrupted writes; a fresh
			// temporary file belongs to a write in progress
			if info, err := dirEntry.Info(); err == nil && now.Sub(info.ModTime()) > time.Hour {
				os.Remove(path)
			}
			continue
		}
		info, err := dirEntry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			if os.Remove(path) == nil {
				removed = true
			}
			continue
		}
		files = append(files, cachedFile{name: name, size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}

	if maxBytes > 0 && total > maxBytes {
		sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
		for _, file := range files {
			if total <= maxBytes {
				break
			}
			if os.Remove(filepath.Join(s.dir, file.name)) == nil {
				total -= file.size
				removed = true
			}
		}
	}

	if !removed {
		return
	}
	s.mu.Lock()
	for name := range s.entries {
		if _, err := os.Stat(filepath.Join(s.dir, name)); errors.Is(err, os.ErrNotExist) {
			delete(s.entries, name)
		}
	}
	s.mu.Unlock()
	s.writeIndex()
}

// afterRead schedules a background revalidation of a file that came from
// disk. Files without metadata were written by an older version of the cache,
// so they are refreshed once to learn their validators and timestamp.
func (s *Store) afterRead(emote *hasherino.Emote, key string) {
	s.mu.Lock()
	cached, ok := s.entries[key]
	stale := !ok || s.now().Sub(cached.FetchedAt) >= s.revalidateAfter
	s.mu.Unlock()
	if stale {
		s.scheduleRevalidate(emote)
	}
}

// scheduleRevalidate starts a background revalidation unless the same emote
// is already being revalidated.
func (s *Store) scheduleRevalidate(emote *hasherino.Emote) {
	key := Key(emote)
	s.mu.Lock()
	if _, busy := s.revalidating[key]; busy {
		s.mu.Unlock()
		return
	}
	s.revalidating[key] = struct{}{}
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.revalidating, key)
			s.mu.Unlock()
		}()
		s.revalidate(emote)
	}()
}

// revalidate performs the conditional request that refreshes a cached file.
func (s *Store) revalidate(emote *hasherino.Emote) {
	key := Key(emote)
	s.mu.Lock()
	cached := s.entries[key]
	var previous *entry
	if cached != nil {
		copied := *cached
		previous = &copied
	}
	s.mu.Unlock()

	data, meta, err := s.download(emote, previous)
	if errors.Is(err, errNotModified) {
		s.mu.Lock()
		if cached := s.entries[key]; cached != nil {
			cached.FetchedAt = s.now()
		}
		s.mu.Unlock()
		s.writeIndex()
		return
	}
	if err != nil {
		// the cached file stays usable; the next load retries
		return
	}

	// a cdn without validators always returns the file; keep the decoded
	// copy when the bytes did not actually change
	if previous != nil {
		if existing, err := os.ReadFile(filepath.Join(s.dir, key)); err == nil && bytes.Equal(existing, data) {
			s.storeBytes(emote, data, meta)
			return
		}
	}

	s.storeBytes(emote, data, meta)

	s.mu.Lock()
	update := s.onUpdate
	s.mu.Unlock()
	if update != nil {
		update(emote)
	}
}

// storeBytes writes the file of the emote and records its metadata.
func (s *Store) storeBytes(emote *hasherino.Emote, data []byte, meta *entry) {
	if s.dir == "" {
		return
	}
	key := Key(emote)
	if err := writeFileAtomically(filepath.Join(s.dir, key), data); err != nil {
		log.Printf("Failed to cache emote %s: %s", key, err)
		return
	}

	url, _ := emoteURL(emote)
	stored := &entry{FetchedAt: s.now(), Size: int64(len(data)), URL: url}
	if meta != nil {
		stored.ETag = meta.ETag
		stored.LastModified = meta.LastModified
	}
	s.mu.Lock()
	s.entries[key] = stored
	s.mu.Unlock()
	s.writeIndex()
}

// download fetches the file of the emote, sending the validators of the
// previous copy when there is one.
func (s *Store) download(emote *hasherino.Emote, previous *entry) ([]byte, *entry, error) {
	url, err := emoteURL(emote)
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	if previous != nil {
		if previous.ETag != "" {
			request.Header.Set("If-None-Match", previous.ETag)
		}
		if previous.LastModified != "" {
			request.Header.Set("If-Modified-Since", previous.LastModified)
		}
	}

	s.slots <- struct{}{}
	defer func() { <-s.slots }()

	s.mu.Lock()
	client := s.client
	s.mu.Unlock()

	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return nil, nil, errNotModified
	}
	if response.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("downloading emote %s failed: %s", emote.Name, response.Status)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, nil, err
	}
	return data, &entry{
		URL:          url,
		ETag:         response.Header.Get("ETag"),
		LastModified: response.Header.Get("Last-Modified"),
	}, nil
}

// touch makes a cached file count as used. Reading a file does not reliably
// update its access time, so the modification time the cleanup uses is
// refreshed instead. It is throttled because it costs a syscall.
func (s *Store) touch(key string) {
	if s.dir == "" {
		return
	}
	path := filepath.Join(s.dir, key)
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	now := s.now()
	if now.Sub(info.ModTime()) < touchInterval {
		return
	}
	os.Chtimes(path, now, now)
}

func (s *Store) path(emote *hasherino.Emote) string {
	return filepath.Join(s.dir, Key(emote))
}

// loadIndex reads the metadata of the cached files. A missing or unreadable
// index is not fatal: the files are still usable, only their validators are
// learned again on the next load.
func (s *Store) loadIndex() {
	data, err := os.ReadFile(filepath.Join(s.dir, indexName))
	if err != nil {
		return
	}
	var document indexDocument
	if err := json.Unmarshal(data, &document); err != nil {
		log.Printf("Failed to read the emote cache index: %s", err)
		return
	}
	if document.Version != indexVersion {
		return
	}
	for key, cached := range document.Entries {
		if cached != nil {
			s.entries[key] = cached
		}
	}
}

// writeIndex stores the metadata of the cached files atomically. Concurrent
// calls are coalesced: the first writer takes a snapshot, later calls only
// mark it dirty, and the writer repeats until no newer snapshot is pending.
func (s *Store) writeIndex() {
	if s.dir == "" {
		return
	}
	s.indexMu.Lock()
	if s.indexWriting {
		s.indexPending = true
		s.indexMu.Unlock()
		return
	}
	s.indexWriting = true
	s.indexMu.Unlock()

	for {
		s.writeIndexNow()

		s.indexMu.Lock()
		if !s.indexPending {
			s.indexWriting = false
			s.indexMu.Unlock()
			return
		}
		s.indexPending = false
		s.indexMu.Unlock()
	}
}

// writeIndexNow writes one snapshot of the index without coalescing.
func (s *Store) writeIndexNow() {
	s.mu.Lock()
	document := indexDocument{Version: indexVersion, Entries: make(map[string]*entry, len(s.entries))}
	for key, cached := range s.entries {
		copied := *cached
		document.Entries[key] = &copied
	}
	s.mu.Unlock()

	data, err := json.Marshal(document)
	if err != nil {
		return
	}
	if err := writeFileAtomically(filepath.Join(s.dir, indexName), data); err != nil {
		log.Printf("Failed to write the emote cache index: %s", err)
	}
}

// removeTemporaryFiles removes the leftovers of writes that were interrupted.
func (s *Store) removeTemporaryFiles() {
	dirEntries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, dirEntry := range dirEntries {
		if strings.HasPrefix(dirEntry.Name(), "tmp-") {
			os.Remove(filepath.Join(s.dir, dirEntry.Name()))
		}
	}
}

// writeFileAtomically writes the file through a temporary file, so that
// concurrent readers never see a partially written file.
func writeFileAtomically(path string, data []byte) error {
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
