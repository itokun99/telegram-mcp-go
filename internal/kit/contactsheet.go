package kit

// This file ports contact_sheet.py: labelled image grids so one picture can
// index many. The Python side drew with Pillow and encoded JPEG at quality
// 88; the Go side uses only image/png, image/draw and math, which means two
// deliberate, documented differences:
//
//   - the sheet is encoded as PNG, because image/jpeg in the standard library
//     exposes no quality knob (only DefaultQuality = 75) and no new dependency
//     is allowed; every other observable semantic - grid geometry, cell edge,
//     label strip, label padding, letterboxing, placeholder cells - is ported
//     1:1;
//   - Pillow's LANCZOS resample has no standard-library equivalent (image/draw
//     has no scaler at all), so thumbnails are bilinearly resampled here.
//
// The Pillow-unavailable degradation class has no Go counterpart: PNG/JPEG/GIF
// decoding is built in, so an undecodable tile degrades to the placeholder cell
// exactly as it does in Python.

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	// Registered so image.Decode accepts the formats Telegram hands back.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

const (
	// CellEdgePixels mirrors CELL_EDGE_PIXELS: the square thumbnail area.
	CellEdgePixels = 256
	// LabelStripPixels mirrors LABEL_STRIP_PIXELS: the caption band height.
	LabelStripPixels = 26
	// LabelFontPixels mirrors LABEL_FONT_PIXELS, the nominal label size.
	LabelFontPixels = 15
	// LabelPaddingPixels is the inset of the caption inside its strip, the
	// Python draw.text((left + 5, strip_top + 5)) offset.
	LabelPaddingPixels = 5
	// MaximumColumns mirrors MAXIMUM_COLUMNS, the automatic-layout ceiling.
	MaximumColumns = 4
)

// Palette, mirroring the Python module-level colour constants.
var (
	GridBackground  = color.RGBA{R: 24, G: 24, B: 24, A: 255}
	LabelBackground = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	LabelForeground = color.RGBA{R: 255, G: 255, B: 255, A: 255}
)

// ErrNoTiles is the Go spelling of the Python
// ValueError("A contact sheet needs at least one tile.").
var ErrNoTiles = errors.New("A contact sheet needs at least one tile.")

// Tile is one (image bytes, label) pair. The label is the identifier a caller
// passes back to open that single image at full resolution, which is what makes
// the sheet an index rather than a decoration.
type Tile struct {
	Image []byte
	Label string
}

// GridLayout is the resolved geometry of a sheet: the grid dimensions in cells
// and the resulting pixel size.
type GridLayout struct {
	TileCount  int
	Columns    int
	Rows       int
	Width      int
	Height     int
	CellHeight int
}

// ColumnsFor ports _columns_for: an explicit positive request is capped at the
// tile count, otherwise the grid is as square as possible and never wider than
// MaximumColumns.
func ColumnsFor(tileCount, requestedColumns int) int {
	if requestedColumns > 0 {
		return min(requestedColumns, tileCount)
	}
	return min(MaximumColumns, max(1, int(math.Ceil(math.Sqrt(float64(tileCount))))))
}

// LayoutFor ports the grid arithmetic of build_contact_sheet. A zero tile count
// is ErrNoTiles, as it is in Python.
func LayoutFor(tileCount, requestedColumns int) (GridLayout, error) {
	if tileCount <= 0 {
		return GridLayout{}, ErrNoTiles
	}
	columns := ColumnsFor(tileCount, requestedColumns)
	rows := int(math.Ceil(float64(tileCount) / float64(columns)))
	cellHeight := CellEdgePixels + LabelStripPixels
	return GridLayout{
		TileCount:  tileCount,
		Columns:    columns,
		Rows:       rows,
		Width:      columns * CellEdgePixels,
		Height:     rows * cellHeight,
		CellHeight: cellHeight,
	}, nil
}

// Origin returns the top-left pixel of tile position, laid out row-major.
func (l GridLayout) Origin(position int) image.Point {
	return image.Point{
		X: (position % l.Columns) * CellEdgePixels,
		Y: (position / l.Columns) * l.CellHeight,
	}
}

// BuildContactSheet ports build_contact_sheet: it lays (image, label) pairs out
// row-major into one labelled PNG. requestedColumns <= 0 selects the automatic
// layout. A tile that cannot be decoded degrades to a placeholder cell rather
// than failing the whole sheet.
func BuildContactSheet(tiles []Tile, requestedColumns int) ([]byte, error) {
	layout, err := LayoutFor(len(tiles), requestedColumns)
	if err != nil {
		return nil, err
	}

	sheet := image.NewRGBA(image.Rect(0, 0, layout.Width, layout.Height))
	draw.Draw(sheet, sheet.Bounds(), image.NewUniform(GridBackground), image.Point{}, draw.Src)

	for position, tile := range tiles {
		drawCell(sheet, tile, layout.Origin(position))
	}

	var rendered bytes.Buffer
	if err := png.Encode(&rendered, sheet); err != nil {
		return nil, fmt.Errorf("kit: encoding contact sheet: %w", err)
	}
	return rendered.Bytes(), nil
}

// drawCell ports _draw_cell: a letterboxed thumbnail, then a black caption
// strip under it with the label inset by LabelPaddingPixels.
func drawCell(sheet *image.RGBA, tile Tile, origin image.Point) {
	if thumbnail, ok := fitWithinCell(tile.Image); ok {
		width, height := thumbnail.Bounds().Dx(), thumbnail.Bounds().Dy()
		left := origin.X + (CellEdgePixels-width)/2
		top := origin.Y + (CellEdgePixels-height)/2
		draw.Draw(
			sheet,
			image.Rect(left, top, left+width, top+height),
			thumbnail,
			thumbnail.Bounds().Min,
			draw.Over,
		)
	}
	// An undecodable tile leaves the grid background the sheet was filled
	// with, which is the Python placeholder.

	stripTop := origin.Y + CellEdgePixels
	strip := image.Rect(origin.X, stripTop, origin.X+CellEdgePixels, stripTop+LabelStripPixels)
	draw.Draw(sheet, strip, image.NewUniform(LabelBackground), image.Point{}, draw.Src)
	drawLabel(sheet, image.Point{origin.X + LabelPaddingPixels, stripTop + LabelPaddingPixels}, tile.Label)
}

// fitWithinCell ports _fit_within_cell: scale by the tighter of the two axis
// ratios, round each side to the nearest even-tie integer and keep at least one
// pixel. A decode failure reports false, which is the placeholder path.
func fitWithinCell(data []byte) (image.Image, bool) {
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	bounds := decoded.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, false
	}
	scale := math.Min(
		float64(CellEdgePixels)/float64(bounds.Dx()),
		float64(CellEdgePixels)/float64(bounds.Dy()),
	)
	width := max(1, roundHalfToEven(float64(bounds.Dx())*scale))
	height := max(1, roundHalfToEven(float64(bounds.Dy())*scale))
	return resizeBilinear(decoded, width, height), true
}

// roundHalfToEven is Python's round(): halfway cases go to the even neighbour,
// which the Go math.Round (half away from zero) does not reproduce.
func roundHalfToEven(value float64) int {
	lower := math.Floor(value)
	remainder := value - lower
	switch {
	case remainder > 0.5:
		return int(lower) + 1
	case remainder < 0.5:
		return int(lower)
	default:
		if int(lower)%2 == 0 {
			return int(lower)
		}
		return int(lower) + 1
	}
}

// resizeBilinear resamples src to width x height with a triangle filter, the
// standard-library stand-in for Pillow's LANCZOS. The result is premultiplied
// RGBA, so draw.Over composites it over the cell background.
func resizeBilinear(src image.Image, width, height int) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	xRatio := float64(sourceWidth) / float64(width)
	yRatio := float64(sourceHeight) / float64(height)

	for y := 0; y < height; y++ {
		sourceY := (float64(y)+0.5)*yRatio - 0.5
		y0, wy := sampleAxis(sourceY, sourceHeight)
		for x := 0; x < width; x++ {
			sourceX := (float64(x)+0.5)*xRatio - 0.5
			x0, wx := sampleAxis(sourceX, sourceWidth)

			var r, g, b, a float64
			for dy := 0; dy < 2; dy++ {
				weightY := wy
				if dy == 0 {
					weightY = 1 - wy
				}
				for dx := 0; dx < 2; dx++ {
					weightX := wx
					if dx == 0 {
						weightX = 1 - wx
					}
					weight := weightX * weightY
					if weight == 0 {
						continue
					}
					pixel := color.NRGBAModel.Convert(src.At(bounds.Min.X+x0+dx, bounds.Min.Y+y0+dy)).(color.NRGBA)
					alpha := float64(pixel.A) / 255
					r += weight * float64(pixel.R) * alpha
					g += weight * float64(pixel.G) * alpha
					b += weight * float64(pixel.B) * alpha
					a += weight * alpha
				}
			}

			offset := dst.PixOffset(x, y)
			if a <= 0 {
				continue
			}
			// Un-premultiply so the stored pixels read back as the source
			// colours, matching a direct RGB copy of an opaque tile.
			dst.Pix[offset+0] = clampChannel(r / a)
			dst.Pix[offset+1] = clampChannel(g / a)
			dst.Pix[offset+2] = clampChannel(b / a)
			dst.Pix[offset+3] = clampChannel(a * 255)
		}
	}
	return dst
}

// sampleAxis maps a source coordinate onto the pair of neighbouring texels and
// their weights, clamping at the edges.
func sampleAxis(value float64, extent int) (int, float64) {
	if value <= 0 {
		return 0, 0
	}
	lower := int(math.Floor(value))
	if lower >= extent-1 {
		return extent - 2, 1
	}
	return lower, value - float64(lower)
}

func clampChannel(value float64) uint8 {
	rounded := math.Round(value)
	switch {
	case rounded <= 0:
		return 0
	case rounded >= 255:
		return 255
	default:
		return uint8(rounded)
	}
}
