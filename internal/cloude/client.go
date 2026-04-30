package cloude

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sensebox/internal/devices"
	"sensebox/internal/mqtt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	reconnectDelay = 5 * time.Second
	pingInterval   = 30 * time.Second
	writeTimeout   = 10 * time.Second
)

// --- Протокол сообщений ---

type MessageType string

const (
	MsgDeviceUpdate  MessageType = "device_update"   // хаб -> сервер: состояние устройства
	MsgDeviceJoined  MessageType = "device_joined"   // хаю -> сервер: новое устройство
	MsgDevideLeft    MessageType = "device_left"     // хаб -> сервер: устройство ушло
	MsgCommand       MessageType = "command"         // сервер -> хаб: управление устройством
	MsgPermitJoin    MessageType = "permit_join"     // сервер -> хаб: разрешить добавление
	MsgPermitJoinAck MessageType = "permit_join_ack" // хаб -> сервер: подтверждение
	MsgPing          MessageType = "ping"
	MsgPong          MessageType = "pong"
)

type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Хаб -> Сервер: обновление устройства
type DeviceUpdatePayload struct {
	Device devices.Device `json:"device"`
}

// Хаб -> Сервер: новое устройство добавлено
type DeviceJoinedPayload struct {
	FriendlyName string `josn:"friendly_name"`
	IEEE         string `json:"ieee"`
}

// Сервер -> Хаб: команда устройству
type CommandPayload struct {
	Device  string         `json:"device"`
	Topic   string         `json:"topic"`
	Payload map[string]any `json:"payload"`
}

// Сервер -> Хаб: разрегить добавление устройств
type PermitJoinPayload struct {
	Time   int    `json:"time"`   // секуды, 0 = запретить
	Device string `json:"device"` // конкретное устройство или "" = все
}

// --- Client ---

type Client struct {
	serverURL   string
	token       string
	registry    *devices.Registry
	mqtt        *mqtt.Client
	mu          sync.Mutex
	conn        *websocket.Conn
	isConnected bool
	send        chan Message
	ctx         context.Context
	cancel      context.CancelFunc
}

func New(serverURL, token string, registry *devices.Registry, mqttClient *mqtt.Client) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		serverURL: serverURL,
		token:     token,
		registry:  registry,
		mqtt:      mqttClient,
		send:      make(chan Message, 64),
		ctx:       ctx,
		cancel:    cancel,
	}

	// Каждое изменение устройство -> очередь отправки
	registry.OnChange(func(_, new devices.Device) {
		c.publishDeviceUpdate(new)
	})

	return c
}

func (c *Client) Start() { go c.loop() }
func (c *Client) Stop()  { c.cancel() }

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

// NotifyDeviceJoined - вызывается из main когда bridge/event = device_joined
func (c *Client) NotifyDeviceJoined(friendlyName, ieee string) {
	payload, _ := json.Marshal(DeviceJoinedPayload{
		FriendlyName: friendlyName,
		IEEE:         ieee,
	})
	c.enqueue(Message{Type: MsgDeviceJoined, Payload: payload})
}

// NotifyDeviceLeft - вызывается из main когда bridge/event = device_left
func (c *Client) NotifyDeviceLeft(friendlyName, ieee string) {
	payload, _ := json.Marshal(DeviceJoinedPayload{
		FriendlyName: friendlyName,
		IEEE:         ieee,
	})
	c.enqueue(Message{Type: MsgDevideLeft, Payload: payload})
}

func (c *Client) publishDeviceUpdate(d devices.Device) {
	payload, err := json.Marshal(DeviceUpdatePayload{Device: d})
	if err != nil {
		return
	}
	c.enqueue(Message{Type: MsgDeviceUpdate, Payload: payload})
}

func (c *Client) enqueue(msg Message) {
	select {
	case c.send <- msg:
	default:
		log.Println("[cloud] send buffer full, dropping message")
	}
}

// --- Основной цикл ---

func (c *Client) loop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		conn, err := c.connect()
		if err != nil {
			log.Printf("[cloud] connect error: %v, retry in %s", err, reconnectDelay)
			time.Sleep(reconnectDelay)
			continue
		}

		c.mu.Lock()
		c.conn = conn
		c.isConnected = true
		c.mu.Unlock()

		log.Println("[cloud] connected")
		c.syncAll()

		done := make(chan struct{})
		go c.readLoop(conn, done)
		c.writeLoop(conn, done)

		c.mu.Lock()
		c.isConnected = false
		c.mu.Unlock()

		log.Println("[cloud] disconnected, reconnecting...")
		time.Sleep(reconnectDelay)
	}
}

func (c *Client) connect() (*websocket.Conn, error) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer"+c.token)
	conn, _, err := websocket.DefaultDialer.DialContext(c.ctx, c.serverURL, headers)
	return conn, err
}

func (c *Client) readLoop(conn *websocket.Conn, done chan struct{}) {
	defer close(done)
	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				log.Printf("[cloud] read error: %v", err)
			}
			return
		}
		c.handleMessage(msg)
	}
}

func (c *Client) writeLoop(conn *websocket.Conn, done chan struct{}) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-c.ctx.Done():
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteJSON(Message{Type: MsgPing}); err != nil {
				log.Printf("[cloud] ping error: %v", err)
				return
			}
		case msg := <-c.send:
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteJSON(msg); err != nil {
				log.Printf("[cloud] write error: %v", err)
				select {
				case c.send <- msg:
				default:
				}
				return
			}
		}
	}
}

// --- Обработка входящих сообщений от сервера ---

func (c *Client) handleMessage(msg Message) {
	switch msg.Type {
	case MsgPong:
	case MsgCommand:
		var cmd CommandPayload
		if err := json.Unmarshal(msg.Payload, &cmd); err != nil {
			log.Printf("[cloud] bad command: %v", err)
			return
		}
		c.executeCommand(cmd)
	case MsgPermitJoin:
		var pj PermitJoinPayload
		if err := json.Unmarshal(msg.Payload, &pj); err != nil {
			log.Printf("[cloud] bad permit_join: %v", err)
			return
		}
		c.executePermitJoin(pj)
	default:
		log.Printf("[cloud] unknown type: %s", msg.Type)
	}
}

// executeCommand публикует команду а MQTT -> Zigbee2MQTT -> устройства
func (c *Client) executeCommand(cmd CommandPayload) {
	log.Printf("[cloud] command device=%s", cmd.Device)
	payload, _ := json.Marshal(cmd.Payload)
	topic := cmd.Topic
	if topic == "" {
		topic = "zigbee2maqtt/" + cmd.Device + "/set"
	}
	c.mqtt.Publish(topic, string(payload))
}

// executePermitJoin разрешает/запрещает добавление новых устройств
func (c *Client) executePermitJoin(pj PermitJoinPayload) {
	log.Printf("[cloud] permit_join time=%d device=%q", pj.Time, pj.Device)

	payload, _ := json.Marshal(map[string]any{
		"time":   pj.Time,
		"device": pj.Device,
	})
	c.mqtt.Publish("zigbee2mqtt/bridge/request/permit_join", string(payload))

	// Подтверждаем серверу
	ack, _ := json.Marshal(map[string]any{
		"time":   pj.Time,
		"device": pj.Device,
	})
	c.enqueue(Message{Type: MsgPermitJoinAck, Payload: ack})
}

// syncAll отправляет текущее состояние всех устройств после подключения
func (c *Client) syncAll() {
	all := c.registry.All()
	for _, d := range all {
		c.publishDeviceUpdate(d)
	}
	log.Printf("[cloud] synced %d devices", len(all))
}
