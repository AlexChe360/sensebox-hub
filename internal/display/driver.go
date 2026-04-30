package display

import (
	"image"
	"image/color"
	"log"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

const (
	Width  = 240
	Height = 320
)

// ILI9341
const (
	cmdSoftReset       = 0x01
	cmdSleepOut        = 0x11
	cmdDisplayOn       = 0x29
	cmdColumnAddrSet   = 0x2A
	cmdRowAddrSet      = 0x2B
	cmdMemoryWrite     = 0x2C
	cmdMemoryAccessCtl = 0x36
	cmdPixelFormat     = 0x3A
)

// Driver - низкоуровневый драйвер ILI9341 по SPI.
type Driver struct {
	port spi.PortCloser
	conn spi.Conn
	dc   gpio.PinOut
	rst  gpio.PinOut
	cs   gpio.PinOut
}

// Config - пины и SPI настройки.
type Config struct {
	SPIPort string
	DCPin   string
	RSTPin  string
}

func NewDriver(cfg Config) (*Driver, error) {
	// Инициализация periph.io
	if _, err := host.Init(); err != nil {
		return nil, err
	}

	// SPI порт
	port, err := spireg.Open(cfg.SPIPort)
	if err != nil {
		return nil, err
	}

	conn, err := port.Connect(8*physic.MegaHertz, spi.Mode0, 8)
	if err != nil {
		port.Close()
		return nil, err
	}

	// GPIO пины
	dc := gpioreg.ByName(cfg.DCPin)
	if dc == nil {
		return nil, err
	}
	rst := gpioreg.ByName(cfg.RSTPin)
	if rst == nil {
		return nil, err
	}

	d := &Driver{
		port: port,
		conn: conn,
		dc:   dc,
		rst:  rst,
	}

	if err := d.init(); err != nil {
		return nil, err
	}

	log.Println("[display] ILI9341 initialized")
	return d, nil
}

func (d *Driver) init() error {
	// Hard reset
	d.rst.Out(gpio.Low)
	time.Sleep(10 * time.Millisecond)
	d.rst.Out(gpio.High)
	time.Sleep(120 * time.Millisecond)

	// Sort reset
	d.writeCmd(cmdSoftReset)
	time.Sleep(150 * time.Millisecond)

	// Sleep out
	d.writeCmd(cmdSleepOut)
	time.Sleep(500 * time.Millisecond)

	// Pixel format: 16 bit (RGB565)
	d.writeCmd(cmdPixelFormat)
	d.writeData([]byte{0x55})

	// Memory access: Row/Col order, RGB
	d.writeCmd(cmdMemoryAccessCtl)
	d.writeData([]byte{0x00})

	d.writeCmd(0x21)

	d.writeCmd(cmdDisplayOn)
	time.Sleep(100 * time.Millisecond)

	return nil
}

// DrawImage выводит image.RGBA на экран (конвертирует в RGB565)
func (d *Driver) DrawImage(img *image.RGBA) {
	d.setWindow(0, 0, Width-1, Height-1)
	d.writeCmd(cmdMemoryWrite)

	buf := make([]byte, Width*Height*2)
	idx := 0
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			c := img.RGBAAt(x, y)
			rgb := toRGB565(c)
			buf[idx] = byte(rgb >> 8)
			buf[idx+1] = byte(rgb)
			idx += 2
		}
	}

	d.dc.Out(gpio.High)
	chunkSize := 4096
	for i := 0; i < len(buf); i += chunkSize {
		end := i + chunkSize
		if end > len(buf) {
			end = len(buf)
		}
		d.conn.Tx(buf[i:end], nil)
	}
}

func (d *Driver) writeCmd(cmd byte) {
	d.dc.Out(gpio.Low)
	d.conn.Tx([]byte{cmd}, nil)
}

func (d *Driver) writeData(data []byte) {
	d.dc.Out(gpio.High)
	d.conn.Tx(data, nil)
}

func (d *Driver) Close() {
	d.port.Close()
}

func (d *Driver) setWindow(x0, y0, x1, y1 int) {
	d.writeCmd(cmdColumnAddrSet)
	d.writeData([]byte{
		byte(x0 >> 8), byte(x0),
		byte(x1 >> 8), byte(x1),
	})
	d.writeCmd(cmdRowAddrSet)
	d.writeData([]byte{
		byte(y0 >> 8), byte(y0),
		byte(y1 >> 8), byte(y1),
	})
}

func toRGB565(c color.RGBA) uint16 {
	r := uint16(c.R>>3) << 11
	g := uint16(c.G>>2) << 5
	b := uint16(c.B >> 3)
	return r | g | b
}
