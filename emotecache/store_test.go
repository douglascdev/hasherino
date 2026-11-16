package emotecache

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

// serveEmotes points the cache at a test server for the duration of the test.
func serveEmotes(t *testing.T, handler http.HandlerFunc) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	oldURL := emoteURL
	emoteURL = func(*hasherino.Emote) (string, error) {
		return server.URL + "/emote", nil
	}
	t.Cleanup(func() { emoteURL = oldURL })
	return &requests
}

// serveBytes serves data for every request and counts the requests.
func serveBytes(t *testing.T, data []byte) *atomic.Int32 {
	t.Helper()
	return serveEmotes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	})
}

func testEmote() *hasherino.Emote {
	return &hasherino.Emote{Id: "emote-id", Source: hasherino.SevenTV, Name: "emote", Animated: true, Version: "v1"}
}

func TestKeyIncludesVersionAndSanitizesIt(t *testing.T) {
	emote := testEmote()
	if got, want := Key(emote), "1-emote-id-v1.gif"; got != want {
		t.Fatalf("unexpected key: got %q, want %q", got, want)
	}

	emote.Version = "../../escape"
	if got, want := Key(emote), "1-emote-id-....escape.gif"; got != want {
		t.Fatalf("expected the version to be sanitized: got %q, want %q", got, want)
	}

	twitch := &hasherino.Emote{Id: "25", Source: hasherino.Twitch}
	if got, want := Key(twitch), "0-25.img"; got != want {
		t.Fatalf("unexpected twitch key: got %q, want %q", got, want)
	}

	if got := Key(nil); got != "" {
		t.Errorf("expected no key for a nil emote, got %q", got)
	}
}

func TestLoadPersistsFileAcrossReopen(t *testing.T) {
	data := []byte("emote file")
	requests := serveBytes(t, data)
	dir := t.TempDir()

	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	emote := testEmote()
	got, cached, err := first.Load(emote)
	if err != nil {
		t.Fatalf("expected the first load to succeed: %s", err)
	}
	if cached {
		t.Error("expected the first load to download the emote")
	}
	if string(got) != string(data) {
		t.Errorf("unexpected data: got %q, want %q", got, data)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected 1 request, got %d", requests.Load())
	}

	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, cached, err = second.Load(emote)
	if err != nil {
		t.Fatalf("expected the second load to succeed: %s", err)
	}
	if !cached {
		t.Error("expected the second load to read the cached file")
	}
	if string(got) != string(data) {
		t.Errorf("unexpected cached data: got %q, want %q", got, data)
	}
	if requests.Load() != 1 {
		t.Errorf("expected the cached load to skip the network, got %d requests", requests.Load())
	}
}

func TestRevalidateKeepsFileOnNotModified(t *testing.T) {
	dir := t.TempDir()
	data := []byte("emote file")
	var requests atomic.Int32
	oldURL := emoteURL
	emoteURL = func(*hasherino.Emote) (string, error) {
		return "http://unused/emote", nil
	}
	t.Cleanup(func() { emoteURL = oldURL })

	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.SetClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.Header.Get("If-None-Match") == `"v1"` {
			return response(http.StatusNotModified, nil, `"v1"`), nil
		}
		return response(http.StatusOK, data, `"v1"`), nil
	})})

	emote := testEmote()
	if _, _, err := store.Load(emote); err != nil {
		t.Fatal(err)
	}
	before := storedEntry(store, emote).FetchedAt

	store.revalidate(emote)

	if requests.Load() != 2 {
		t.Fatalf("expected a conditional request, got %d requests", requests.Load())
	}
	if got := readFile(t, store.path(emote)); string(got) != string(data) {
		t.Errorf("expected the file to stay unchanged, got %q", got)
	}
	if !storedEntry(store, emote).FetchedAt.After(before) {
		t.Error("expected the revalidation to update the timestamp")
	}
}

func TestRevalidateReplacesChangedFile(t *testing.T) {
	dir := t.TempDir()
	first := []byte("first version")
	second := []byte("second version")
	var requests atomic.Int32
	oldURL := emoteURL
	emoteURL = func(*hasherino.Emote) (string, error) {
		return "http://unused/emote", nil
	}
	t.Cleanup(func() { emoteURL = oldURL })

	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.SetClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.Header.Get("If-None-Match") == `"v1"` {
			return response(http.StatusOK, second, `"v2"`), nil
		}
		return response(http.StatusOK, first, `"v1"`), nil
	})})

	emote := testEmote()
	if _, _, err := store.Load(emote); err != nil {
		t.Fatal(err)
	}

	updated := make(chan *hasherino.Emote, 1)
	store.SetOnUpdate(func(emote *hasherino.Emote) { updated <- emote })
	store.revalidate(emote)

	if got := readFile(t, store.path(emote)); string(got) != string(second) {
		t.Errorf("expected the changed file to replace the old one, got %q", got)
	}
	select {
	case got := <-updated:
		if got.Id != emote.Id {
			t.Errorf("unexpected updated emote: %+v", got)
		}
	case <-time.After(time.Second):
		t.Error("expected the update callback to be called")
	}
	if cached := storedEntry(store, emote); cached.ETag != `"v2"` {
		t.Errorf("expected the new etag to be stored, got %q", cached.ETag)
	}
}

func TestInvalidateForcesFreshDownload(t *testing.T) {
	requests := serveBytes(t, []byte("emote file"))
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	emote := testEmote()

	if _, _, err := store.Load(emote); err != nil {
		t.Fatal(err)
	}
	store.Invalidate(emote)
	if store.Cached(emote) {
		t.Fatal("expected the file to be removed")
	}
	if _, cached, err := store.Load(emote); err != nil {
		t.Fatal(err)
	} else if cached {
		t.Error("expected the load after invalidation to download again")
	}
	if requests.Load() != 2 {
		t.Errorf("expected 2 downloads, got %d", requests.Load())
	}
}

func TestLoadDownloadsWhenCachedFileIsEmpty(t *testing.T) {
	requests := serveBytes(t, []byte("emote file"))
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	emote := testEmote()
	if err := os.WriteFile(store.path(emote), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, cached, err := store.Load(emote); err != nil {
		t.Fatalf("expected the load to recover from the empty file: %s", err)
	} else if cached {
		t.Error("expected the empty file to be downloaded again")
	}
	if requests.Load() != 1 {
		t.Errorf("expected 1 download, got %d", requests.Load())
	}
}

func TestCleanRemovesUnusedFiles(t *testing.T) {
	serveBytes(t, []byte("emote file"))
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.SetLimits(time.Hour, CacheMaxBytes, store.revalidateAfter)

	oldEmote := testEmote()
	oldEmote.Id = "old"
	newEmote := testEmote()
	newEmote.Id = "new"
	for _, emote := range []*hasherino.Emote{oldEmote, newEmote} {
		if _, _, err := store.Load(emote); err != nil {
			t.Fatal(err)
		}
	}

	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(store.path(oldEmote), stale, stale); err != nil {
		t.Fatal(err)
	}
	store.Clean()

	if _, err := os.Stat(store.path(oldEmote)); !errors.Is(err, os.ErrNotExist) {
		t.Error("expected the unused file to be removed")
	}
	if _, err := os.Stat(store.path(newEmote)); err != nil {
		t.Errorf("expected the used file to be kept: %s", err)
	}
	if storedEntry(store, oldEmote) != nil {
		t.Error("expected the metadata of the removed file to be dropped")
	}
}

func TestCleanRemovesLeastRecentlyUsedFilesAboveSizeLimit(t *testing.T) {
	serveBytes(t, make([]byte, 500))
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.SetLimits(store.maxAge, 600, store.revalidateAfter)

	oldEmote := testEmote()
	oldEmote.Id = "old"
	newEmote := testEmote()
	newEmote.Id = "new"
	for _, emote := range []*hasherino.Emote{oldEmote, newEmote} {
		if _, _, err := store.Load(emote); err != nil {
			t.Fatal(err)
		}
	}

	older := time.Now().Add(-2 * time.Minute)
	newer := time.Now().Add(-time.Minute)
	os.Chtimes(store.path(oldEmote), older, older)
	os.Chtimes(store.path(newEmote), newer, newer)
	store.Clean()

	if _, err := os.Stat(store.path(oldEmote)); !errors.Is(err, os.ErrNotExist) {
		t.Error("expected the least recently used file to be removed")
	}
	if _, err := os.Stat(store.path(newEmote)); err != nil {
		t.Errorf("expected the most recently used file to be kept: %s", err)
	}
}

func TestOpenRemovesTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp-leftover")
	if err := os.WriteFile(tmp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Error("expected the leftover temporary file to be removed")
	}
}

func TestDefaultDirHonorsCacheOverride(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HASHERINO_CACHE_DIR", base)
	dir, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, folderName); dir != want {
		t.Errorf("unexpected cache dir: got %q, want %q", dir, want)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, body []byte, etag string) *http.Response {
	header := make(http.Header)
	if etag != "" {
		header.Set("ETag", etag)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// storedEntry returns a copy of the metadata of a cached emote.
func storedEntry(store *Store, emote *hasherino.Emote) *entry {
	store.mu.Lock()
	defer store.mu.Unlock()
	cached, ok := store.entries[Key(emote)]
	if !ok {
		return nil
	}
	copied := *cached
	return &copied
}
