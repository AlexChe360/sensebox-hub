package display

import (
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/inconsolata"
	"golang.org/x/image/math/fixed"
)

// Цветовая палитра
var (
	ColorBG       = color.RGBA{R: 10, G: 10, B: 30, A: 255}    // тёмно-синий фон
	ColorPrimary  = color.RGBA{R: 255, G: 255, B: 255, A: 255} // белый
	ColorAccent   = color.RGBA{R: 0, G: 200, B: 120, A: 255}   // зелёный — online
	ColorWarning  = color.RGBA{R: 255, G: 180, B: 0, A: 255}   // жёлтый
	ColorDanger   = color.RGBA{R: 220, G: 50, B: 50, A: 255}   // красный — offline
	ColorDivider  = color.RGBA{R: 50, G: 50, B: 80, A: 255}    // разделитель
	ColorPairCode = color.RGBA{R: 255, G: 220, B: 0, A: 255}   // жёлтый — код сопряжения
	ColorSubtext  = color.RGBA{R: 150, G: 150, B: 180, A: 255} // серый — подписи
)

// Renderer рисует UI на image.RGBA.
type Renderer struct {
	img *image.RGBA
}

func NewRenderer() *Renderer {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	return &Renderer{img: img}
}

func (r *Renderer) Image() *image.RGBA {
	return r.img
}

// Clear заливает фон.
func (r *Renderer) Clear() {
	draw.Draw(r.img, r.img.Bounds(), &image.Uniform{ColorBG}, image.Point{}, draw.Src)
}

// Text рисует строку в позиции (x, y).
func (r *Renderer) Text(x, y int, text string, c color.RGBA) {
	d := &font.Drawer{
		Dst:  r.img,
		Src:  &image.Uniform{c},
		Face: inconsolata.Bold8x16,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)},
	}
	d.DrawString(text)
}

// BigText рисует крупный текст (масштабирует basicfont вручную).
func (r *Renderer) BigText(x, y int, text string, c color.RGBA, scale int) {
	for i, ch := range text {
		r.drawScaledChar(x+i*(7*scale+2), y, byte(ch), c, scale)
	}
}

func (r *Renderer) drawScaledChar(x, y int, ch byte, c color.RGBA, scale int) {
	// Рисуем символ в маленький image через font.Drawer, потом масштабируем
	face := basicfont.Face7x13
	w := face.Advance
	h := face.Height

	tmp := image.NewRGBA(image.Rect(0, 0, w, h))
	d := &font.Drawer{
		Dst:  tmp,
		Src:  &image.Uniform{c},
		Face: face,
		Dot:  fixed.Point26_6{X: 0, Y: fixed.I(face.Ascent)},
	}
	d.DrawString(string(ch))

	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			_, _, _, a := tmp.At(col, row).RGBA()
			if a > 0x7fff {
				for sy := 0; sy < scale; sy++ {
					for sx := 0; sx < scale; sx++ {
						px := x + col*scale + sx
						py := y + row*scale + sy
						if px >= 0 && px < Width && py >= 0 && py < Height {
							r.img.SetRGBA(px, py, c)
						}
					}
				}
			}
		}
	}
}

// HLine рисует горизонтальную линию.
func (r *Renderer) HLine(y int, c color.RGBA) {
	for x := 0; x < Width; x++ {
		r.img.SetRGBA(x, y, c)
	}
}

// Rect рисует заполненный прямоугольник.
func (r *Renderer) Rect(x, y, w, h int, c color.RGBA) {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			r.img.SetRGBA(x+dx, y+dy, c)
		}
	}
}

// Icon рисует иконку из набора ASCII-иконок (псевдоиконки для OLED).
// Для настоящих иконок используй PNG → []byte и рисуй пиксельно.
func (r *Renderer) Icon(x, y int, icon string, c color.RGBA) {
	r.Text(x, y, icon, c)
}

// StatusDot рисует кружок статуса.
func (r *Renderer) StatusDot(x, y int, online bool) {
	c := ColorAccent
	if !online {
		c = ColorDanger
	}
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			if dx*dx+dy*dy <= 16 {
				r.img.SetRGBA(x+dx, y+dy, c)
			}
		}
	}
}
