package kit

// Grid layout math and rendering over in-memory images only: no network, no
// Telegram, every tile is encoded in-process.

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func solidPNG(t *testing.T, width, height int, fill color.RGBA) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			canvas.SetRGBA(x, y, fill)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatalf("encoding %dx%d tile: %v", width, height, err)
	}
	return encoded.Bytes()
}

func decodeSheet(t *testing.T, data []byte) image.Image {
	t.Helper()
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding sheet: %v", err)
	}
	return decoded
}

func pixelAt(sheet image.Image, x, y int) color.RGBA {
	r, g, b, a := sheet.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func TestContactSheetLayoutMath(t *testing.T) {
	cases := []struct {
		name      string
		tiles     int
		columns   int
		wantCols  int
		wantRows  int
		wantWidth int
		wantHigh  int
	}{
		{"one tile is a one by one grid", 1, 0, 1, 1, CellEdgePixels, CellEdgePixels + LabelStripPixels},
		{"six tiles wrap into three columns and two rows", 6, 0, 3, 2, 3 * CellEdgePixels, 2 * (CellEdgePixels + LabelStripPixels)},
		{"seven tiles reserve a third row for the remainder", 7, 0, 3, 3, 3 * CellEdgePixels, 3 * (CellEdgePixels + LabelStripPixels)},
		{"seventeen tiles are capped at four columns", 17, 0, 4, 5, 4 * CellEdgePixels, 5 * (CellEdgePixels + LabelStripPixels)},
		{"explicit columns override the automatic layout", 6, 2, 2, 3, 2 * CellEdgePixels, 3 * (CellEdgePixels + LabelStripPixels)},
		{"columns never exceed the number of tiles", 1, 8, 1, 1, CellEdgePixels, CellEdgePixels + LabelStripPixels},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout, err := LayoutFor(tc.tiles, tc.columns)
			if err != nil {
				t.Fatalf("LayoutFor(%d, %d): %v", tc.tiles, tc.columns, err)
			}
			if layout.Columns != tc.wantCols || layout.Rows != tc.wantRows {
				t.Errorf("grid = %dx%d, want %dx%d", layout.Columns, layout.Rows, tc.wantCols, tc.wantRows)
			}
			if layout.Width != tc.wantWidth || layout.Height != tc.wantHigh {
				t.Errorf("size = %dx%d, want %dx%d", layout.Width, layout.Height, tc.wantWidth, tc.wantHigh)
			}
			if got := ColumnsFor(tc.tiles, tc.columns); got != tc.wantCols {
				t.Errorf("ColumnsFor(%d, %d) = %d, want %d", tc.tiles, tc.columns, got, tc.wantCols)
			}
		})
	}
}

func TestContactSheetPlacesEverySampleImageRowMajor(t *testing.T) {
	const (
		tiles   = 9
		columns = 3
	)
	palette := []color.RGBA{
		{R: 200, G: 40, B: 40, A: 255},
		{R: 40, G: 200, B: 40, A: 255},
		{R: 40, G: 40, B: 200, A: 255},
		{R: 200, G: 200, B: 40, A: 255},
		{R: 200, G: 40, B: 200, A: 255},
		{R: 40, G: 200, B: 200, A: 255},
		{R: 255, G: 128, B: 0, A: 255},
		{R: 128, G: 0, B: 255, A: 255},
		{R: 0, G: 128, B: 128, A: 255},
	}

	sheetTiles := make([]Tile, 0, tiles)
	for position, fill := range palette {
		sheetTiles = append(sheetTiles, Tile{
			Image: solidPNG(t, 100, 100, fill),
			Label: string(rune('A' + position)),
		})
	}

	sheetBytes, err := BuildContactSheet(sheetTiles, 0)
	if err != nil {
		t.Fatalf("BuildContactSheet: %v", err)
	}
	sheet := decodeSheet(t, sheetBytes)

	cellHeight := CellEdgePixels + LabelStripPixels
	if got := sheet.Bounds().Size(); got != (image.Point{X: 3 * CellEdgePixels, Y: 3 * cellHeight}) {
		t.Fatalf("sheet size = %v, want %dx%d", got, 3*CellEdgePixels, 3*cellHeight)
	}

	for position, want := range palette {
		centre := image.Point{
			X: (position%columns)*CellEdgePixels + CellEdgePixels/2,
			Y: (position/columns)*cellHeight + CellEdgePixels/2,
		}
		if got := pixelAt(sheet, centre.X, centre.Y); got != want {
			t.Errorf("tile %d centre = %v, want %v", position, got, want)
		}
	}
}

func TestContactSheetLetterboxesWideSources(t *testing.T) {
	green := color.RGBA{R: 0, G: 200, B: 0, A: 255}
	sheetBytes, err := BuildContactSheet([]Tile{{Image: solidPNG(t, 800, 200, green), Label: "wide"}}, 0)
	if err != nil {
		t.Fatalf("BuildContactSheet: %v", err)
	}
	sheet := decodeSheet(t, sheetBytes)

	centre := pixelAt(sheet, CellEdgePixels/2, CellEdgePixels/2)
	corner := pixelAt(sheet, 2, 2)
	if centre != green {
		t.Errorf("centre = %v, want the source colour %v", centre, green)
	}
	if corner != GridBackground {
		t.Errorf("corner = %v, want the letterbox background %v", corner, GridBackground)
	}
}

func TestContactSheetDegradesUnreadableTilesToPlaceholders(t *testing.T) {
	sheetBytes, err := BuildContactSheet([]Tile{{Image: []byte("not-an-image"), Label: "broken"}}, 0)
	if err != nil {
		t.Fatalf("BuildContactSheet: %v", err)
	}
	sheet := decodeSheet(t, sheetBytes)

	want := image.Point{X: CellEdgePixels, Y: CellEdgePixels + LabelStripPixels}
	if got := sheet.Bounds().Size(); got != want {
		t.Fatalf("sheet size = %v, want %v", got, want)
	}
	if centre := pixelAt(sheet, CellEdgePixels/2, CellEdgePixels/2); centre != GridBackground {
		t.Errorf("placeholder centre = %v, want %v", centre, GridBackground)
	}
}

func TestContactSheetDrawsLabelStripBeneathEveryCell(t *testing.T) {
	tile := solidPNG(t, 64, 64, color.RGBA{R: 10, G: 10, B: 10, A: 255})
	sheetBytes, err := BuildContactSheet([]Tile{{Image: tile, Label: "1820385360168986609"}}, 0)
	if err != nil {
		t.Fatalf("BuildContactSheet: %v", err)
	}
	sheet := decodeSheet(t, sheetBytes)

	stripRow := CellEdgePixels + LabelStripPixels/2
	bright := 0
	for x := 0; x < CellEdgePixels; x += 2 {
		pixel := pixelAt(sheet, x, stripRow)
		if int(pixel.R)+int(pixel.G)+int(pixel.B) > 300 {
			bright++
		}
	}
	if bright == 0 {
		t.Fatalf("no label pixels in row %d of the strip", stripRow)
	}

	stripBackground := pixelAt(sheet, CellEdgePixels-2, CellEdgePixels+1)
	if stripBackground != LabelBackground {
		t.Errorf("strip background = %v, want %v", stripBackground, LabelBackground)
	}
}

func TestContactSheetRejectsAnEmptyTileSequence(t *testing.T) {
	if _, err := BuildContactSheet(nil, 0); !errors.Is(err, ErrNoTiles) {
		t.Errorf("error = %v, want ErrNoTiles", err)
	}
	if _, err := BuildContactSheet([]Tile{}, 2); !errors.Is(err, ErrNoTiles) {
		t.Errorf("error = %v, want ErrNoTiles", err)
	}
}

func TestContactSheetRoundHalfToEvenMatchesPythonRound(t *testing.T) {
	cases := map[float64]int{0.5: 0, 1.5: 2, 2.5: 2, 3.5: 4, -0.5: 0, -1.5: -2, 64.0: 64, 63.68: 64}
	for value, want := range cases {
		if got := roundHalfToEven(value); got != want {
			t.Errorf("roundHalfToEven(%v) = %d, want %d", value, got, want)
		}
	}
}
