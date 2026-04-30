package mqtt

import (
	"log"

	paho "github.com/eclipse/paho.mqtt.golang"
)

type Client struct {
	paho     paho.Client
	handlers map[string]func(topic string, payload []byte)
}

func New(broker, clientID string) *Client {
	c := &Client{
		handlers: make(map[string]func(topic string, payload []byte)),
	}
	opts := paho.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			log.Printf("[mqtt] connection lost: %v", err)
		})
	c.paho = paho.NewClient(opts)
	return c
}

func (c *Client) Connect() error {
	token := c.paho.Connect()
	token.Wait()
	return token.Error()
}

func (c *Client) Subscribe(topic string, handler func(string, []byte)) {
	c.handlers[topic] = handler
	c.paho.Subscribe(topic, 0, func(_ paho.Client, msg paho.Message) {
		handler(msg.Topic(), msg.Payload())
	})
}

func (c *Client) Publish(topic, payload string) {
	token := c.paho.Publish(topic, 0, false, payload)
	token.Wait()
	if err := token.Error(); err != nil {
		log.Printf("[mqtt] publish error: %v", err)
	}
}

// Пепеподписываемся при реконнекте
func (c *Client) onConnect(_ paho.Client) {
	log.Printf("[mqtt] connected")
	for topic, handle := range c.handlers {
		c.Subscribe(topic, handle)
	}
}
