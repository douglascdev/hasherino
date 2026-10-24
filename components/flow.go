package components

import "fyne.io/fyne/v2"

// FlowLayout arranges its objects from left to right, wrapping onto a new line
// once the available width is used up. Objects are centered vertically within
// their line, so text can sit next to taller objects such as emotes.
type FlowLayout struct {
	horizontalPadding float32
	verticalPadding   float32
	width             float32
}

func NewFlowLayout(horizontalPadding float32, verticalPadding float32) *FlowLayout {
	return &FlowLayout{
		horizontalPadding: horizontalPadding,
		verticalPadding:   verticalPadding,
	}
}

type flowItem struct {
	object fyne.CanvasObject
	size   fyne.Size
}

func (f *FlowLayout) lines(objects []fyne.CanvasObject, width float32) [][]flowItem {
	lines := [][]flowItem{}
	line := []flowItem{}
	used := float32(0)

	for _, object := range objects {
		if object == nil || !object.Visible() {
			continue
		}

		size := object.MinSize()
		if width > 0 && size.Width > width {
			size.Width = width
		}
		if len(line) > 0 && width > 0 && used+f.horizontalPadding+size.Width > width {
			lines = append(lines, line)
			line = []flowItem{}
			used = 0
		}
		if len(line) > 0 {
			used += f.horizontalPadding
		}
		used += size.Width
		line = append(line, flowItem{object: object, size: size})
	}
	if len(line) > 0 {
		lines = append(lines, line)
	}

	return lines
}

func (f *FlowLayout) lineSize(line []flowItem) fyne.Size {
	size := fyne.NewSize(0, 0)
	for i, item := range line {
		if i > 0 {
			size.Width += f.horizontalPadding
		}
		size.Width += item.size.Width
		size.Height = fyne.Max(size.Height, item.size.Height)
	}
	return size
}

// MinSize returns the size needed to display the objects. The height is
// calculated using the width of the last Layout call, as wrapping depends on
// how much horizontal space is available. The width is that of the widest
// object, because every other object can be wrapped onto its own line.
func (f *FlowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	min := fyne.NewSize(0, 0)
	for _, object := range objects {
		if object == nil || !object.Visible() {
			continue
		}
		min.Width = fyne.Max(min.Width, object.MinSize().Width)
	}
	for i, line := range f.lines(objects, f.width) {
		if i > 0 {
			min.Height += f.verticalPadding
		}
		min.Height += f.lineSize(line).Height
	}
	return min
}

func (f *FlowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	f.width = size.Width

	y := float32(0)
	for i, line := range f.lines(objects, size.Width) {
		if i > 0 {
			y += f.verticalPadding
		}
		lineHeight := f.lineSize(line).Height

		x := float32(0)
		for _, item := range line {
			item.object.Move(fyne.NewPos(x, y+(lineHeight-item.size.Height)/2))
			item.object.Resize(item.size)
			x += item.size.Width + f.horizontalPadding
		}
		y += lineHeight
	}
}
