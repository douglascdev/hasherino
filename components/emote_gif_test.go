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

// decodeAnimation decodes gif bytes the way the emote cache would.
func decodeAnimation(t *testing.T, data []byte) *gifAnimation {
	t.Helper()
	src, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	animation := buildAnimation(src)
	if animation == nil {
		t.Fatal("expected the gif to build an animation")
	}
	return animation
}

func TestBuildAnimationCompositesEveryFrame(t *testing.T) {
	animation := decodeAnimation(t, animatedGifBytes(t))

	if len(animation.frames) != 4 {
		t.Errorf("expected 4 composited frames, got %d", len(animation.frames))
	}
	for i, frame := range animation.frames {
		if frame.Bounds() != image.Rect(0, 0, 2, 2) {
			t.Errorf("expected frame %d to cover the gif bounds, got %v", i, frame.Bounds())
		}
	}
	if animation.loopCount != 0 {
		t.Errorf("expected the loop count to carry over, got %d", animation.loopCount)
	}
	for i, delay := range animation.delays {
		if delay != 100*time.Millisecond {
			t.Errorf("expected frame %d to be displayed for 100ms, got %v", i, delay)
		}
	}
}

func TestBuildAnimationFloorsTooFastFrames(t *testing.T) {
	var buf bytes.Buffer
	frames := []*image.Paletted{
		image.NewPaletted(image.Rect(0, 0, 2, 2), palette.Plan9),
		image.NewPaletted(image.Rect(0, 0, 2, 2), palette.Plan9),
	}
	// delays of 0 and 1 would request 0ms and 10ms display times
	gifData := &gif.GIF{Image: frames, Delay: []int{0, 1}, LoopCount: 0}
	if err := gif.EncodeAll(&buf, gifData); err != nil {
		t.Fatal(err)
	}

	animation := decodeAnimation(t, buf.Bytes())

	for i, delay := range animation.delays {
		if delay < gifMinFrameTime {
			t.Errorf("expected frame %d to be floored to %v, got %v", i, gifMinFrameTime, delay)
		}
	}
}

// newTickingGif returns a running widget that plays the given animation
// without registering it with the global animator, so that tests can drive
// the ticks deterministically.
func newTickingGif(t *testing.T, animation *gifAnimation) *EmoteGif {
	t.Helper()
	app := test.NewApp()
	t.Cleanup(app.Quit)

	widget := newGif()
	widget.emote = &hasherino.Emote{Name: "test"}
	widget.clickCallback = func(string) error { return nil }
	widget.lazyLoad = true
	widget.animation = animation
	widget.loaded = true
	widget.remaining = -1
	widget.lastFrameAt = time.Now()
	widget.running = true
	return widget
}

func TestEmoteGifTickAdvancesFrames(t *testing.T) {
	animation := decodeAnimation(t, animatedGifBytes(t))
	widget := newTickingGif(t, animation)

	// no frame advances before the frame delay passed
	if swap := widget.tick(widget.lastFrameAt.Add(50 * time.Millisecond)); swap != nil {
		t.Fatal("expected no advance before the frame delay")
	}
	if widget.current != 0 {
		t.Errorf("expected the animation to stay on frame 0, got %d", widget.current)
	}

	// advancing after the delay swaps in the next frame
	swap := widget.tick(widget.lastFrameAt.Add(60 * time.Millisecond))
	if swap == nil {
		t.Fatal("expected an advance after the frame delay")
	}
	if widget.current != 1 {
		t.Fatalf("expected the animation to advance to frame 1, got %d", widget.current)
	}
	swap()
	if widget.dst.Image != animation.frames[1] {
		t.Error("expected the swap to display frame 1")
	}

	// wrapping around loops the animation forever
	for range 4 {
		swap = widget.tick(widget.lastFrameAt.Add(110 * time.Millisecond))
		if swap == nil {
			t.Fatal("expected the animation to keep advancing")
		}
		swap()
	}
	if !widget.isRunning() {
		t.Error("expected a looping animation to keep running")
	}
	if widget.dst.Image != animation.frames[widget.current] {
		t.Error("expected the swap to display the current frame")
	}
}

func TestEmoteGifTickStopsAfterFinalLoop(t *testing.T) {
	animation := decodeAnimation(t, animatedGifBytes(t))
	widget := newTickingGif(t, animation)
	widget.remaining = 1 // play the animation a single time

	// the ticks advance through the frames 1 to 3
	for i := 1; i <= 3; i++ {
		swap := widget.tick(widget.lastFrameAt.Add(110 * time.Millisecond))
		if swap == nil {
			t.Fatal("expected the animation to advance through every frame")
		}
		swap()
		if widget.current != i {
			t.Fatalf("expected the animation to advance to frame %d, got %d", i, widget.current)
		}
	}

	// the next advance wraps around, which finishes the single loop and
	// keeps the final frame visible
	finalSwap := widget.tick(widget.lastFrameAt.Add(110 * time.Millisecond))
	if finalSwap == nil {
		t.Fatal("expected the final frame to be swapped in")
	}
	finalSwap()
	if widget.dst.Image != animation.frames[3] {
		t.Error("expected the final frame to remain visible")
	}
	if widget.isRunning() {
		t.Error("expected the animation to stop after its final loop")
	}

	// ticks of a finished animation do nothing
	if swap := widget.tick(time.Now()); swap != nil {
		t.Error("expected the finished animation to not swap frames")
	}
}

func TestEmoteGifTickSkipsFramesAfterStall(t *testing.T) {
	animation := decodeAnimation(t, animatedGifBytes(t))
	widget := newTickingGif(t, animation)

	// a long stall accumulates debt far beyond the catch up limit, the
	// animation must not burst through every frame it missed
	swap := widget.tick(widget.lastFrameAt.Add(10 * time.Second))
	if swap == nil {
		t.Fatal("expected the animation to advance once after the stall")
	}
	swap()
	if widget.current != 1 {
		t.Errorf("expected the stalled animation to skip ahead a single frame, got %d", widget.current)
	}
}

func TestEmoteGifTickStopsStoppedAnimations(t *testing.T) {
	animation := decodeAnimation(t, animatedGifBytes(t))
	widget := newTickingGif(t, animation)
	widget.Stop()

	if swap := widget.tick(time.Now()); swap != nil {
		t.Error("expected a stopped animation to not swap frames")
	}
	if widget.isRunning() {
		t.Error("expected the stopped animation to be finished")
	}
	if widget.animation != nil {
		t.Error("expected the stopped animation to release its frames")
	}
}

func TestEmoteGifStopsAndRestartsViaAnimator(t *testing.T) {
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

	widget.Start()
	if !widget.isRunning() {
		t.Fatal("expected the animation to run")
	}

	widget.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for widget.isRunning() && time.Now().Before(deadline) {
		time.Sleep(gifFrameTick)
	}
	if widget.isRunning() {
		t.Fatal("timed out waiting for the animation to stop")
	}
	if widget.animation != nil {
		t.Error("expected the stopped gif to release its frames")
	}

	// starting again reloads the gif and runs the animation
	widget.Start()
	if !widget.isRunning() {
		t.Fatal("expected the restarted animation to run")
	}
	widget.runLock.RLock()
	frames := len(widget.animation.frames)
	widget.runLock.RUnlock()
	if frames != 4 {
		t.Errorf("expected the restarted gif to reload its 4 frames, got %d", frames)
	}

	widget.Stop()
	deadline = time.Now().Add(2 * time.Second)
	for widget.isRunning() && time.Now().Before(deadline) {
		time.Sleep(gifFrameTick)
	}
	if widget.isRunning() {
		t.Fatal("timed out waiting for the restarted animation to stop")
	}
}
