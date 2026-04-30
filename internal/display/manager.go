package display

import (
	"fmt"
	"image/color"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
)

// HubState — состояние хаба, определяет какой экран показывать.
type HubState int

const (
	StateSetup   HubState = iota // первый запуск, ничего не настроено
	StatePairing                 // Wi-Fi есть, ждём подключения приложения
	StateOnline                  // всё работает
)

// State — всё что нужно отобразить на экране.
type State struct {
	Hub         HubState
	IP          string
	Time        string
	CloudOnline bool
	DeviceCount int
	PairCode    string
	ShowPair    bool // временный экран сопряжения поверх основного

	// Для экрана setup
	MQTTOnline bool
	Z2MOnline  bool
}

// Manager управляет дисплеем.
type Manager struct {
	driver   *Driver
	renderer *Renderer

	mu    sync.RWMutex
	state State

	quit chan struct{}
}

func NewManager(driver *Driver) *Manager {
	return &Manager{
		driver:   driver,
		renderer: NewRenderer(),
		quit:     make(chan struct{}),
		state: State{
			Hub:      StateSetup, // всегда начинаем с setup
			PairCode: generatePairCode(),
		},
	}
}

func (m *Manager) Start() {
	go m.loop()
	log.Println("[display] manager started")
}

func (m *Manager) Stop() {
	close(m.quit)
}

// --- Публичные методы обновления состояния ---

func (m *Manager) SetCloudStatus(online bool) {
	m.mu.Lock()
	m.state.CloudOnline = online
	m.updateHubState()
	m.mu.Unlock()
}

func (m *Manager) SetMQTTStatus(online bool) {
	m.mu.Lock()
	m.state.MQTTOnline = online
	m.updateHubState()
	m.mu.Unlock()
}

func (m *Manager) SetZ2MStatus(online bool) {
	m.mu.Lock()
	m.state.Z2MOnline = online
	m.updateHubState()
	m.mu.Unlock()
}

func (m *Manager) SetDeviceCount(count int) {
	m.mu.Lock()
	m.state.DeviceCount = count
	m.mu.Unlock()
}

func (m *Manager) ShowPairingScreen(duration time.Duration) {
	m.mu.Lock()
	m.state.ShowPair = true
	m.mu.Unlock()

	time.AfterFunc(duration, func() {
		m.mu.Lock()
		m.state.ShowPair = false
		m.mu.Unlock()
	})
}

func (m *Manager) PairCode() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.PairCode
}

func (m *Manager) RegeneratePairCode() string {
	m.mu.Lock()
	m.state.PairCode = generatePairCode()
	code := m.state.PairCode
	m.mu.Unlock()
	return code
}

// updateHubState вычисляет текущее состояние хаба.
// Вызывается при каждом изменении статусов.
// Должен вызываться под локом mu.
func (m *Manager) updateHubState() {
	switch {
	case !m.state.MQTTOnline || !m.state.Z2MOnline:
		// MQTT или Zigbee2MQTT не работают — setup
		m.state.Hub = StateSetup
	case !m.state.CloudOnline:
		// Локальные сервисы работают, но облако не подключено — pairing
		m.state.Hub = StatePairing
	default:
		// Всё работает
		m.state.Hub = StateOnline
	}
}

// --- Основной цикл ---

func (m *Manager) loop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.quit:
			return
		case <-ticker.C:
			m.update()
			m.render()
		}
	}
}

func (m *Manager) update() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Time = time.Now().Format("15:04:05")
	m.state.IP = getLocalIP()
}

func (m *Manager) render() {
	m.mu.RLock()
	state := m.state
	m.mu.RUnlock()

	r := m.renderer
	r.Clear()

	switch {
	case state.ShowPair:
		m.renderPairingScreen(r, state)
	case state.Hub == StateSetup:
		m.renderSetupScreen(r, state)
	case state.Hub == StatePairing:
		m.renderWaitingScreen(r, state)
	default:
		m.renderMainScreen(r, state)
	}

	m.driver.DrawImage(r.Image())
}

// --- Экран первого запуска ---
//
// ┌──────────────────────────┐
// │  SETUP                   │
// ├──────────────────────────┤
// │                          │
// │  Checking services...    │
// │                          │
// │  [✓] MQTT                │  зелёный если ок
// │  [✗] Zigbee2MQTT         │  красный если нет
// │  [✗] Cloud               │
// │                          │
// ├──────────────────────────┤
// │  IP: 192.168.1.42        │
// │  14:35:22                │
// └──────────────────────────┘

func (m *Manager) renderSetupScreen(r *Renderer, s State) {
	r.Rect(0, 0, Width, 40, color.RGBA{R: 180, G: 100, B: 0, A: 255})
	r.BigText(8, 10, "SETUP", ColorPrimary, 2)
	r.Text(Width-75, 28, s.Time, ColorPrimary)

	r.Text(8, 70, "Checking services...", ColorSubtext)

	m.renderServiceRow(r, 110, "MQTT", s.MQTTOnline)
	m.renderServiceRow(r, 140, "Zigbee2MQTT", s.Z2MOnline)
	m.renderServiceRow(r, 170, "Cloud", s.CloudOnline)

	r.HLine(220, ColorDivider)
	r.Text(8, 245, "IP: "+s.IP, ColorSubtext)
	r.Text(8, 270, s.Time, ColorSubtext)
}

func (m *Manager) renderServiceRow(r *Renderer, y int, name string, ok bool) {
	if ok {
		r.Text(8, y, "[+]", ColorAccent)
	} else {
		r.Text(8, y, "[x]", ColorDanger)
	}
	r.Text(45, y, name, ColorPrimary)
}

// --- Экран ожидания подключения приложения ---
//
// ┌──────────────────────────┐
// │  PAIRING                 │
// ├──────────────────────────┤
// │                          │
// │  Open app and            │
// │  connect to hub:         │
// │                          │
// │      4  8  2  9          │  большой шрифт
// │                          │
// │  IP: 192.168.1.42        │
// │  14:35:22                │
// └──────────────────────────┘

func (m *Manager) renderWaitingScreen(r *Renderer, s State) {
	r.Rect(0, 0, Width, 40, color.RGBA{R: 0, G: 80, B: 160, A: 255})
	r.BigText(8, 14, "PAIRING", ColorPrimary, 2)
	r.Text(Width-75, 28, s.Time, ColorPrimary)

	r.Text(8, 75, "Open app and", ColorSubtext)
	r.Text(8, 100, "connect to hub:", ColorSubtext)

	code := formatPairCode(s.PairCode)
	r.BigText(30, 140, code, ColorPairCode, 4)

	r.HLine(220, ColorDivider)
	r.Text(8, 245, "IP: "+s.IP, ColorSubtext)
	r.Text(8, 270, s.Time, ColorSubtext)
}

// --- Главный экран (всё работает) ---
//
// ┌──────────────────────────┐
// │  SmartHome Hub           │
// ├──────────────────────────┤
// │  IP      192.168.1.42   │
// │  TIME    14:35:22       │
// │  CLOUD   ● Online       │
// │  DEVICES 12             │
// ├──────────────────────────┤
// │  Pair code:             │
// │      4  8  2  9         │
// └──────────────────────────┘

func (m *Manager) renderMainScreen(r *Renderer, s State) {
	r.Rect(0, 0, Width, 28, ColorDivider)
	r.Text(8, 19, "SmartHome Hub", ColorPrimary)

	r.Text(8, 55, "IP", ColorSubtext)
	r.Text(70, 55, s.IP, ColorPrimary)

	r.Text(8, 85, "TIME", ColorSubtext)
	r.Text(70, 85, s.Time, ColorPrimary)

	r.Text(8, 115, "CLOUD", ColorSubtext)
	r.StatusDot(72, 110, s.CloudOnline)
	if s.CloudOnline {
		r.Text(85, 115, "Online", ColorAccent)
	} else {
		r.Text(85, 115, "Offline", ColorDanger)
	}

	r.Text(8, 145, "DEVICES", ColorSubtext)
	r.Text(90, 145, fmt.Sprintf("%d", s.DeviceCount), ColorPrimary)

	r.HLine(165, ColorDivider)

	r.Text(8, 190, "Pair code:", ColorSubtext)
	code := formatPairCode(s.PairCode)
	startX := (Width - len(code)*(7*3+4)) / 2
	r.BigText(startX, 210, code, ColorPairCode, 3)
}

// --- Временный экран сопряжения (новое устройство добавлено) ---
//
// ┌──────────────────────────┐
// │  PAIRING MODE            │
// │                          │
// │  Enter code in           │
// │  your app:               │
// │                          │
// │    4   8   2   9         │  x4 шрифт
// │                          │
// │  Valid for 120 sec       │
// └──────────────────────────┘

func (m *Manager) renderPairingScreen(r *Renderer, s State) {
	r.Rect(0, 0, Width, 35, color.RGBA{R: 0, G: 80, B: 50, A: 255})
	r.Text(8, 24, "PAIRING MODE", ColorPrimary)

	r.Text(20, 80, "Enter code in", ColorSubtext)
	r.Text(20, 100, "your app:", ColorSubtext)

	code := formatPairCode(s.PairCode)
	startX := (Width - len(code)*(7*4+4)) / 2
	r.BigText(startX, 150, code, ColorPairCode, 4)

	r.HLine(240, ColorDivider)
	r.Text(30, 270, "Valid for 120 sec", ColorSubtext)
}

// --- Хелперы ---

func generatePairCode() string {
	return fmt.Sprintf("%04d", rand.Intn(10000))
}

func formatPairCode(code string) string {
	result := ""
	for i, ch := range code {
		if i > 0 {
			result += " "
		}
		result += string(ch)
	}
	return result
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "unknown"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok &&
			!ipnet.IP.IsLoopback() &&
			ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "no IP"
}
