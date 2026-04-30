package api

import (
	"encoding/json"
	"net/http"
	"sensebox/internal/devices"
	"sensebox/internal/mqtt"

	"github.com/gin-gonic/gin"
)

type Server struct {
	registry *devices.Registry
	mqtt     *mqtt.Client
}

func New(registry *devices.Registry, mqttClient *mqtt.Client) *Server {
	return &Server{
		registry: registry,
		mqtt:     mqttClient,
	}
}

func (s *Server) Run(addr string) error {
	r := gin.Default()

	r.GET("/devices", s.listDevices)
	r.GET("/devices/:name", s.getDevice)
	r.POST("/devices/:name/set", s.setDevice)

	return r.Run(addr)
}

// GET /devices - все устройства
func (s *Server) listDevices(c *gin.Context) {
	c.JSON(http.StatusOK, s.registry.All())
}

// GET /devices/:name - одно устройство
func (s *Server) getDevice(c *gin.Context) {
	name := c.Param("name")
	device, ok := s.registry.Get(name)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}
	c.JSON(http.StatusOK, device)
}

// POST /devices/:name/set - отправить команду устройству
// Body: любой JSON, например {"state", "ON"}
func (s *Server) setDevice(c *gin.Context) {
	name := c.Param("name")
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	payload, err := json.Marshal(body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	s.mqtt.Publish("zigbee2mqtt/"+name+"/set", string(payload))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
