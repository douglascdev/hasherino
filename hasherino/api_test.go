package hasherino

import (
	"encoding/json"
	"testing"
)

func TestSTVEmoteDataDimensions(t *testing.T) {
	tests := []struct {
		name   string
		files  string
		width  int
		height int
	}{
		{
			name:   "smallest file defines the intrinsic size",
			files:  `[{"name":"1x.webp","width":32,"height":32},{"name":"2x.webp","width":64,"height":64},{"name":"4x.webp","width":128,"height":128}]`,
			width:  32,
			height: 32,
		},
		{
			name:   "wide emote",
			files:  `[{"name":"1x.webp","width":39,"height":32},{"name":"2x.webp","width":78,"height":64}]`,
			width:  39,
			height: 32,
		},
		{
			name:   "invalid files are skipped",
			files:  `[{"name":"0x.webp","width":0,"height":0},{"name":"1x.webp","width":32,"height":32}]`,
			width:  32,
			height: 32,
		},
		{
			name:   "no valid files",
			files:  `[{"name":"0x.webp","width":0,"height":0}]`,
			width:  0,
			height: 0,
		},
		{
			name:   "no files at all",
			files:  `[]`,
			width:  0,
			height: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var data STVEmoteData
			err := json.Unmarshal([]byte(`{"id":"e","name":"e","host":{"url":"//cdn","files":`+test.files+`}}`), &data)
			if err != nil {
				t.Fatal(err)
			}
			width, height := data.Dimensions()
			if width != test.width || height != test.height {
				t.Errorf("expected dimensions %dx%d, got %dx%d", test.width, test.height, width, height)
			}
		})
	}
}
