# 🏠 sensebox-hub

SenseBox Hub на Go для Raspberry Pi + Sonoff Zigbee Dongle + TFT дисплей 240x320.

---

## Архитектура

```
Zigbee устройства
      ↓
Sonoff Dongle (USB → /dev/ttyUSB0)
      ↓
Zigbee2MQTT
      ↓
Mosquitto (MQTT :1883)
      ↓
sensebox-hub 
   ├── mqtt/client.go         подписка и публикация, автореконнект
   ├── zigbee/handler.go      парсинг payload, автоопределение типа устройства
   ├── devices/registry.go    состояние устройств в памяти + SQLite
   ├── automation/engine.go   правила из config.yaml, работает локально
   ├── cloud/client.go        WebSocket + JWT, двусторонняя связь с сервером
   ├── api/server.go          REST API (Gin) :8080
   └── display/               TFT 240x320 (ILI9341 SPI)
       ├── driver.go           SPI драйвер, RGB565
       ├── renderer.go         рисование текста, иконок, примитивов
       └── manager.go          экраны: главный + сопряжение
```

### Поток данных

```
[датчик сработал]
      ↓
Zigbee2MQTT → MQTT топик zigbee2mqtt/<name>
      ↓
mqtt/client.go — получает сообщение
      ↓
zigbee/handler.go — парсит JSON, определяет тип устройства
      ↓
devices/registry.go — обновляет память + SQLite
      ↓
      ├── automation/engine.go — проверяет правила → mqtt.Publish()
      ├── cloud/client.go      — отправляет update на сервер (WSS)
      └── display/manager.go   — обновляет экран раз в секунду
```

---

## Устройства

Поддерживаемые типы устройств (`DeviceType`):

| Тип | Константа | Поля |
|-----|-----------|------|
| Реле / розетка | `TypeRelay` | `state`, `device_temperature`, `power_outage_count` |
| Штора / жалюзи | `TypeCurtain` | `state`, `position`, `work_state`, `illuminance`, `battery` |
| Климат | `TypeSensor` | `temperature`, `humidity` |
| Движение | `TypeMotion` | `occupancy`, `battery` |
| Контакт | `TypeContact` | `contact`, `battery` |

Тип определяется автоматически по полям MQTT payload в `zigbee/handler.go`.

Реальные устройства из текущей установки:

| Адрес | Тип | Описание |
|-------|-----|----------|
| `0x54ef44100150de59` | `TypeRelay` | Умная розетка |
| `0xa4c138768b8ca3b4` | `TypeCurtain` | Привод шторы |

---

## Структура проекта

```
sensebox-hub/
├── cmd/
│   └── main.go                  точка входа
├── internal/
│   ├── mqtt/
│   │   └── client.go            MQTT клиент
│   ├── zigbee/
│   │   └── handler.go           парсинг Zigbee2MQTT
│   ├── devices/
│   │   └── registry.go          реестр устройств
│   ├── automation/
│   │   └── engine.go            движок автоматизаций
│   ├── cloud/
│   │   └── client.go            WebSocket клиент
│   ├── api/
│   │   └── server.go            REST API
│   └── display/
│       ├── driver.go            SPI драйвер ILI9341
│       ├── renderer.go          рисование UI
│       └── manager.go           управление экранами
├── config/
│   └── config.yaml              вся конфигурация
├── go.mod
└── README.md
```

---

## Требования

- Raspberry Pi (3/4/5)
- Sonoff Zigbee 3.0 USB Dongle (P или E)
- TFT дисплей 240x320 (GMT02-02-08P, ILI9341, SPI)
- Go 1.22+
- Mosquitto
- Zigbee2MQTT

---

## Установка

### 1. Зависимости

```bash
# Mosquitto
sudo apt install mosquitto mosquitto-clients libsqlite3-dev
sudo systemctl enable mosquitto

# Node.js + Zigbee2MQTT
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo bash -
sudo apt install nodejs
sudo git clone https://github.com/Koenkk/zigbee2mqtt.git /opt/zigbee2mqtt
sudo npm install

# Go (ARM64 для Pi 4/5)
wget https://go.dev/dl/go1.25.9.linux-arm64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.25.9.linux-arm64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
```

### 2. Zigbee2MQTT

```yaml
homeassistant:
  enabled: false
 
mqtt:
  base_topic: zigbee2mqtt
  server: mqtt://localhost:1883
 
serial:
  port: /dev/serial/by-id/usb-1a86_USB_Serial-if00-port0
  adapter: zstack
 
frontend:
  enabled: true
  port: 8080
  host: 0.0.0.0
 
advanced:
  log_level: info
  log_output:
    - console
  channel: 25
  transmit_power: 20
  last_seen: epoch
 
permit_join: true
```

> Порт указывается через `by-id` — это надёжнее чем `/dev/ttyUSB0`, не меняется при перезагрузке.

```bash
zigbee2mqtt &

# Проверка — должны появляться сообщения при срабатывании устройств
mosquitto_sub -t 'zigbee2mqtt/#' -v
```



### 3. Подключение дисплея (SPI)

```
Дисплей  Цвет    RPi пин
GND    → чёрный → пин 6  (GND)
VCC    → красный → пин 1  (3.3V)
SCL    → синий   → пин 23 (GPIO11)
SDA    → фиолетовый → пин 19 (GPIO10)
RST    → коричневый → пин 11 (GPIO17)
DC     → белый   → пин 22 (GPIO25)
CS     → серый   → пин 24 (GPIO8)
```

Включить SPI на Pi:
```bash
sudo raspi-config → Interface Options → SPI → Enable
```

```bash
sudo nano /etc/systemd/system/zigbee2mqtt.service
```
```ini
[Unit]
Description=Zigbee2MQTT
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/zigbee2mqtt
ExecStart=/usr/bin/npm start
Restart=on-failure
User=pi

[Install]
WantedBy=multi-user.target
```

### 4. Сборка и запуск

```bash
git clone https://github.com/yourname/sensebox-hub
cd sensebox-hub
go mod tidy
go build -o sensebox ./cmd/main.go
./sensebox
```

---

## Конфигурация

Всё настраивается в `config/config.yaml` без перекомпиляции.

```yaml
mqtt:
  broker: "tcp://localhost:1883"
  client_id: "sensebox-hub"

api:
  port: "8080"

database:
  path: "sensebox.db"

cloud:
  enabled: true
  server_url: "wss://yourserver.com/ws"
  token: "your-jwt-token-here"

display:
  enabled: true
  spi_port: "SPI0.0"
  dc_pin: "GPIO24"
  rst_pin: "GPIO25"

automations:
  - name: "Темно — закрыть штору"
    trigger:
      device: "0xa4c138768b8ca3b4"
      field: "illuminance"
      operator: lt
      value: 20
    action:
      type: mqtt
      topic: "zigbee2mqtt/0xa4c138768b8ca3b4/set"
      payload: '{"state": "CLOSE"}'
```

### Триггеры

| field | устройство | тип | описание |
|-------|------------|-----|----------|
| `state` | реле, штора | string | ON / OFF / OPEN / CLOSE |
| `position` | штора | int | позиция 0-100% |
| `work_state` | штора | string | opening / closing / stop |
| `illuminance` | штора | int | освещённость |
| `device_temperature` | реле | float | температура устройства |
| `power_outage_count` | реле | int | кол-во отключений питания |
| `temperature` | датчик | float | температура воздуха |
| `humidity` | датчик | float | влажность |
| `occupancy` | движение | bool | есть движение |
| `contact` | контакт | bool | закрыт/открыт |
| `battery` | любое | int | заряд % |

### Операторы

| operator | описание |
|----------|----------|
| `eq` | равно + изменилось (по умолчанию) |
| `neq` | не равно |
| `lt` | меньше чем |
| `gt` | больше чем |
| `contains` | содержит строку |

### Действия

| type | описание |
|------|----------|
| `mqtt` | отправить команду устройству |
| `log` | записать в лог |

В `message` доступны плейсхолдеры: `{device}`, `{value}`, `{state}`, `{position}`.

---

## Дисплей

Два экрана, переключаются автоматически.

**Главный** (постоянно):
```
┌──────────────────────────┐
│  SenseBox Hub            │ 
├──────────────────────────┤
│  IP      192.168.1.42    │
│  TIME    14:35:22        │
│  CLOUD   ● Online        │
│  DEVICES 12              │
├──────────────────────────┤
│       Pair code:         |    
│      4  8  2  9          │
└──────────────────────────┘
```

**Сопряжение** (30 сек при старте, вызывается через API):
```
┌──────────────────────────┐
│  PAIRING MODE            │
│                          │
│  Enter code in           │
│  your app:               │
│                          │
│    4   8   2   9         │
│                          │
│  Valid for 120 sec       │
└──────────────────────────┘
```

---

## REST API

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/devices` | все устройства |
| GET | `/devices/:name` | одно устройство |
| POST | `/devices/:name/set` | отправить команду |

```bash
# Все устройства
curl http://localhost:8080/devices

# Состояние розетки
curl http://localhost:8080/devices/0x54ef44100150de59

# Включить розетку
curl -X POST http://localhost:8080/devices/0x54ef44100150de59/set \
  -H "Content-Type: application/json" \
  -d '{"state": "ON"}'

# Открыть штору на 50%
curl -X POST http://localhost:8080/devices/0xa4c138768b8ca3b4/set \
  -H "Content-Type: application/json" \
  -d '{"position": 50}'

# Закрыть штору
curl -X POST http://localhost:8080/devices/0xa4c138768b8ca3b4/set \
  -H "Content-Type: application/json" \
  -d '{"state": "CLOSE"}'
```

---

## Протокол облака (WebSocket)

Хаб подключается к серверу с заголовком `Authorization: Bearer <JWT>`.

```json
// Хаб → Сервер: обновление устройства
{
  "type": "device_update",
  "payload": {
    "device": {
      "friendly_name": "0xa4c138768b8ca3b4",
      "type": "curtain",
      "state": "OPEN",
      "position": 100,
      "battery": 100,
      "illuminance": 97,
      "last_seen": "2026-04-29T19:13:06Z"
    }
  }
}

// Сервер → Хаб: команда
{
  "type": "command",
  "payload": {
    "device": "0xa4c138768b8ca3b4",
    "topic": "zigbee2mqtt/0xa4c138768b8ca3b4/set",
    "payload": { "position": 50 }
  }
}
```

При реконнекте хаб автоматически синхронизирует состояние всех устройств.

---

## Автозапуск (systemd)

```ini
# /etc/systemd/system/sensebox.service
[Unit]
Description=SenseBox Hub
After=network.target mosquitto.service

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi/sensebox
ExecStart=/home/pi/sensebox/sensebox
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable sensebox
sudo systemctl start sensebox
sudo journalctl -u sensebox -f
```

---

## Зависимости

| Пакет | Назначение |
|-------|-----------|
| `eclipse/paho.mqtt.golang` | MQTT клиент |
| `gin-gonic/gin` | REST API |
| `gorilla/websocket` | WebSocket для облака |
| `mattn/go-sqlite3` | SQLite хранилище |
| `tidwall/gjson` | парсинг JSON |
| `golang.org/x/image` | шрифты для дисплея |
| `periph.io/x/conn` | SPI/GPIO для дисплея |
| `periph.io/x/host` | инициализация periph |
| `gopkg.in/yaml.v3` | конфиг |