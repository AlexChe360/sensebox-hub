package cloude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sensebox/internal/devices"
	"sensebox/internal/mqtt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	reconnectDelay  = 5 * time.Second
	pingInterval    = 30 * time.Second
	writeTimeout    = 10 * time.Second
	credentialsFile = "data/hub_credentials.json"
)

// --- Протокол сообщений ---

type MessageType string

const (
	MsgDeviceUpdate  MessageType = "device_update"
	MsgDeviceJoined  MessageType = "device_joined"
	MsgDevideLeft    MessageType = "device_left"
	MsgCommand       MessageType = "command"
	MsgPermitJoin    MessageType = "permit_join"
	MsgPairStart     MessageType = "pair_start"
	MsgPermitJoinAck MessageType = "permit_join_ack"
	MsgPing          MessageType = "ping"
	MsgPong          MessageType = "pong"
)

type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type DeviceUpdatePayload struct {
	Device devices.Device `json:"device"`
}

type DeviceJoinedPayload struct {
	FriendlyName string `json:"friendly_name"`
	IEEE         string `json:"ieee"`
}

type CommandPayload struct {
	Device  string         `json:"device"`
	Topic   string         `json:"topic"`
	Payload map[string]any `json:"payload"`
}

type PermitJoinPayload struct {
	Time   int    `json:"time"`
	Device string `json:"device"`
}

// --- Credentials ---

type Credentials struct {
	HubID       string `json:"hub_id"`
	Token       string `json:"token"`
	PairingCode string `json:"pairing_code"`
	ExpiresAt   int64  `json:"expires_at"`
}

// --- Client ---

type Client struct {
	serverURL   string
	serial      string
	registry    *devices.Registry
	mqtt        *mqtt.Client
	mu          sync.Mutex
	conn        *websocket.Conn
	isConnected bool
	send        chan Message
	ctx         context.Context
	cancel      context.CancelFunc
	creds       *Credentials
}

func New(serverURL, serial string, registry *devices.Registry, mqttClient *mqtt.Client) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		serverURL: serverURL,
		serial:    serial,
		registry:  registry,
		mqtt:      mqttClient,
		send:      make(chan Message, 64),
		ctx:       ctx,
		cancel:    cancel,
	}

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

func (c *Client) PairingCode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.creds != nil {
		return c.creds.PairingCode
	}
	return ""
}

func (c *Client) NotifyDeviceJoined(friendlyName, ieee string) {
	payload, _ := json.Marshal(DeviceJoinedPayload{
		FriendlyName: friendlyName,
		IEEE:         ieee,
	})
	c.enqueue(Message{Type: MsgDeviceJoined, Payload: payload})
}

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

// --- Регистрация и credentials ---

func (c *Client) ensureRegistered() error {
	// Пробуем загрузить сохранённые credentials
	if creds, err := loadCredentials(); err == nil {
		// Проверяем не протух ли токен (с запасом 1 день)
		if creds.ExpiresAt > time.Now().Unix()+86400 {
			c.creds = creds
			log.Printf("[cloud] loaded credentials: hub_id=%s", creds.HubID)
			return nil
		}
		log.Println("[cloud] token expired, re-registering...")
	}

	// Регистрируемся на сервере
	creds, err := c.register()
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	c.creds = creds
	if err := saveCredentials(creds); err != nil {
		log.Printf("[cloud] warning: could not save credentials: %v", err)
	}

	log.Printf("[cloud] registered: hub_id=%s pairing_code=%s", creds.HubID, creds.PairingCode)
	return nil
}

func (c *Client) register() (*Credentials, error) {
	url := strings.Replace(c.serverURL, "wss://", "https://", 1)
	url = strings.Replace(url, "ws://", "http://", 1)
	// Убираем /ws/hub если есть
	url = strings.TrimSuffix(url, "/ws/hub")
	url = strings.TrimSuffix(url, "/ws")
	url += "/api/hub/register"

	body, _ := json.Marshal(map[string]string{
		"serial":   c.serial,
		"name":     "SenseBox Pi",
		"firmware": "0.1.0",
	})

	resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return nil, fmt.Errorf("register failed: %s %s", resp.Status, string(respBody))
	}

	var result struct {
		HubID       string `json:"hub_id"`
		Token       string `json:"token"`
		PairingCode string `json:"pairing_code"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &Credentials{
		HubID:       result.HubID,
		Token:       result.Token,
		PairingCode: result.PairingCode,
		ExpiresAt:   time.Now().Unix() + result.ExpiresIn,
	}, nil
}

func loadCredentials() (*Credentials, error) {
	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

func saveCredentials(creds *Credentials) error {
	dir := filepath.Dir(credentialsFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(creds, "", "  ")
	return os.WriteFile(credentialsFile, data, 0600)
}

// --- Основной цикл ---

func (c *Client) loop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		if err := c.ensureRegistered(); err != nil {
			log.Printf("[cloud] registration error: %v, retry in %s", err, reconnectDelay)
			time.Sleep(reconnectDelay)
			continue
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
		if c.creds.PairingCode == "" {
			c.refreshPairingCode()
		}

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
	wsURL := c.serverURL
	if !strings.Contains(wsURL, "/ws") {
		wsURL += "/ws/hub"
	}
	wsURL += "?token=" + c.creds.Token + "&hub_id=" + c.creds.HubID
	conn, _, err := websocket.DefaultDialer.DialContext(c.ctx, wsURL, nil)
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
	log.Printf("[cloud] recv: type=%s payload=%s", msg.Type, string(msg.Payload))
	switch msg.Type {
	case MsgPong:
	case "connected":
		log.Println("[cloud] server acknowledged connection")
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
	case MsgPairStart:
		pj := PermitJoinPayload{Time: 60}
		if len(msg.Payload) > 0 {
			json.Unmarshal(msg.Payload, &pj)
		}
		if pj.Time == 0 {
			pj.Time = 60
		}
		log.Printf("[cloud] pair_start: allowing join for %ds", pj.Time)
		c.executePermitJoin(pj)
	default:
		log.Printf("[cloud] unknown type: %s", msg.Type)
	}
}

func (c *Client) executeCommand(cmd CommandPayload) {
	log.Printf("[cloud] command device=%s", cmd.Device)
	payload, _ := json.Marshal(cmd.Payload)
	topic := cmd.Topic
	if topic == "" {
		topic = "zigbee2mqtt/" + cmd.Device + "/set"
	}
	c.mqtt.Publish(topic, string(payload))
}

func (c *Client) executePermitJoin(pj PermitJoinPayload) {
	log.Printf("[cloud] permit_join time=%d device=%q", pj.Time, pj.Device)

	payload, _ := json.Marshal(map[string]any{
		"time":   pj.Time,
		"device": pj.Device,
	})
	c.mqtt.Publish("zigbee2mqtt/bridge/request/permit_join", string(payload))

	ack, _ := json.Marshal(map[string]any{
		"time":   pj.Time,
		"device": pj.Device,
	})
	c.enqueue(Message{Type: MsgPermitJoinAck, Payload: ack})
}

func (c *Client) syncAll() {
	all := c.registry.All()
	for _, d := range all {
		c.publishDeviceUpdate(d)
	}
	log.Printf("[cloud] synced %d devices", len(all))
}

func (c *Client) refreshPairingCode() {
	if c.creds == nil {
		return
	}

	url := strings.Replace(c.serverURL, "wss://", "https://", 1)
	url = strings.Replace(url, "ws://", "http://", 1)
	url = strings.TrimSuffix(url, "/ws/hub")
	url = strings.TrimSuffix(url, "/ws")
	url += "/api/hub/" + c.creds.HubID + "/pairing-code"

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		log.Printf("[cloud] pairing code request error: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.creds.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[cloud] pairing code error: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[cloud] pairing code failed: %s %s", resp.Status, string(body))
		return
	}

	var result struct {
		PairingCode string `json:"pairing_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[cloud] pairing code parse error: %v", err)
		return
	}

	c.mu.Lock()
	c.creds.PairingCode = result.PairingCode
	c.mu.Unlock()

	saveCredentials(c.creds)
	log.Printf("[cloud] new pairing code: %s", result.PairingCode)
}
