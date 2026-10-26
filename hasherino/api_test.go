package hasherino

import (
	"encoding/json"
	"testing"
)

func TestHelixUserEmoteParsesAndDetectsAnimated(t *testing.T) {
	tests := []struct {
		name     string
		formats  string
		animated bool
	}{
		{
			name:     "static emote",
			formats:  `["static"]`,
			animated: false,
		},
		{
			name:     "animated emote",
			formats:  `["animated"]`,
			animated: true,
		},
		{
			name:     "static and animated emote",
			formats:  `["static", "animated"]`,
			animated: true,
		},
		{
			name:     "no formats",
			formats:  `[]`,
			animated: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sample := `{
				"emote_set_id": "",
				"emote_type": "hypetrain",
				"format": ` + test.formats + `,
				"id": "304420818",
				"name": "HypeLol",
				"owner_id": "477339272",
				"scale": ["1.0", "2.0", "3.0"],
				"theme_mode": ["light", "dark"]
			}`
			var emote HelixUserEmote
			if err := json.Unmarshal([]byte(sample), &emote); err != nil {
				t.Fatal(err)
			}

			if emote.ID != "304420818" {
				t.Errorf("expected the id to parse, got %q", emote.ID)
			}
			if emote.Name != "HypeLol" {
				t.Errorf("expected the name to parse, got %q", emote.Name)
			}
			if emote.OwnerID != "477339272" {
				t.Errorf("expected the owner id to parse, got %q", emote.OwnerID)
			}
			if emote.EmoteType != "hypetrain" {
				t.Errorf("expected the emote type to parse, got %q", emote.EmoteType)
			}
			if emote.Animated() != test.animated {
				t.Errorf("expected animated %v, got %v", test.animated, emote.Animated())
			}
		})
	}
}

func TestHelixUserEmotesParsesPagination(t *testing.T) {
	sample := `{
		"data": [{"id": "304420818", "name": "HypeLol", "format": ["static"]}],
		"template": "https://static-cdn.jtvnw.net/emoticons/v2/{{id}}/{{format}}/{{theme_mode}}/{{scale}}",
		"pagination": {"cursor": "eyJiIjpudWxsLJxhIjoiIn0 gf5"}
	}`
	var emotes HelixUserEmotes
	if err := json.Unmarshal([]byte(sample), &emotes); err != nil {
		t.Fatal(err)
	}

	if len(emotes.Data) != 1 {
		t.Fatalf("expected 1 emote, got %d", len(emotes.Data))
	}
	if emotes.Data[0].Name != "HypeLol" {
		t.Errorf("expected the emote to parse, got %+v", emotes.Data[0])
	}
	if emotes.Pagination.Cursor != "eyJiIjpudWxsLJxhIjoiIn0 gf5" {
		t.Errorf("expected the cursor to parse, got %q", emotes.Pagination.Cursor)
	}
	if emotes.Template != "https://static-cdn.jtvnw.net/emoticons/v2/{{id}}/{{format}}/{{theme_mode}}/{{scale}}" {
		t.Errorf("expected the template to parse, got %q", emotes.Template)
	}
}

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
