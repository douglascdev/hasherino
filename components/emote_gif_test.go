package components

import (
	"bytes"
	"image"
	"image/color/palette"
	"image/gif"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

// animatedGifBytes builds a gif with multiple frames and delays, so that
// animations started with it stay running until they are stopped.
func animatedGifBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	var images []*image.Paletted
	var delays []int
	for range 4 {
		images = append(images, image.NewPaletted(image.Rect(0, 0, 2, 2), palette.Plan9))
		delays = append(delays, 10)
	}
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: images, Delay: delays, LoopCount: 0}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEmoteGifKeepsPlaceholderAfterStop(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	downloadEmote = func(*hasherino.Emote) ([]byte, error) {
		return animatedGifBytes(t), nil
	}
	t.Cleanup(func() {
		downloadEmote = func(*hasherino.Emote) ([]byte, error) {
			return nil, errEmoteDownloadsDisabled
		}
	})

	emote := &hasherino.Emote{Id: "stoppable", Source: hasherino.SevenTV, Name: "stoppable", Animated: true}
	widget, err := NewEmoteGif(emote, func(string) error { return nil }, true)
	if err != nil {
		t.Fatal(err)
	}

	// stopped animations must keep a placeholder gif instead of nil, so
	// that draws queued before the stop never dereference nil
	widget.Start()
	widget.Stop()

	deadline := time.Now().Add(time.Second)
	for widget.isRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if widget.isRunning() {
		t.Fatal("timed out waiting for the animation to stop")
	}
	if widget.src == nil {
		t.Fatal("expected the stopped gif to keep its placeholder")
	}

	// starting again reloads the real gif and runs the animation
	widget.Start()
	if !widget.isRunning() {
		t.Fatal("expected the restarted animation to run")
	}
	if len(widget.src.Image) != 4 {
		t.Errorf("expected the restarted gif to reload its frames, got %d", len(widget.src.Image))
	}

	widget.Stop()
	deadline = time.Now().Add(time.Second)
	for widget.isRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if widget.isRunning() {
		t.Fatal("timed out waiting for the restarted animation to stop")
	}
}
