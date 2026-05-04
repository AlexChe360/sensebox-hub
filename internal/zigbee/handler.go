package zigbee

import (
	"log"
	"sensebox/internal/devices"
	"strings"

	"github.com/tidwall/gjson"
)

const prefix = "zigbee2mqtt/"

// EventType - тип события из bridge/*
type EventType = string

const (
	EventDeviceJoined    EventType = "device_joined"
	EventDeviceLeft      EventType = "device_left"
	EventDeviceAnnonce   EventType = "device_annonce"
	EventPermitJoin      EventType = "permit_join"
	EventUnknown         EventType = "unknown"
	EventDeviceInterview EventType = "device_interview"
)

// BridgeEvent - события из zigbee2mqtt/bridge/event
type BridgeEvent struct {
	Type         EventType
	FriendlyName string
	IEEE         string // уцникальный адрес устройства
	DeviceType   devices.DeviceType
	Model        string
}

// ParseMessage разбирает MQTT сообщения от Zigbee2MQTT
// Возвращает либо Device (обычное устройство), либо BridgeEvent (bridge/*)
func ParseMessage(topic string, payload []byte) (devices.Device, bool) {
	if !strings.HasPrefix(topic, prefix) {
		return devices.Device{}, false
	}

	name := strings.TrimPrefix(topic, prefix)

	// Служебный топик обрабатываем отдельно
	if strings.HasPrefix(name, "bridge/") {
		return devices.Device{}, false
	}

	p := string(payload)

	d := devices.Device{
		FriendlyName:      name,
		LinkQuality:       gjson.Get(p, "linkquality").Int(),
		Battery:           gjson.Get(p, "battery").Int(),
		State:             gjson.Get(p, "state").String(),
		DeviceTemperature: gjson.Get(p, "device_temperature").Float(),
		PowerOutageCount:  gjson.Get(p, "power_outage_count").Int(),
		Position:          gjson.Get(p, "position").Int(),
		WorkState:         gjson.Get(p, "work_state").String(),
		CurtainStatus:     gjson.Get(p, "curtain_state").Int(),
		Illuminance:       gjson.Get(p, "illuminance").Int(),
		TotalTime:         gjson.Get(p, "total_time").Int(),
		Temperature:       gjson.Get(p, "temperature").Float(),
		Humidity:          gjson.Get(p, "humidity").Float(),
		Occupancy:         gjson.Get(p, "occupancy").Bool(),
		Contact:           gjson.Get(p, "contact").Bool(),
	}

	if gjson.Get(p, "water_leak").Exists() {
		if gjson.Get(p, "water_leak").Bool() {
			d.State = "ON"
		} else {
			d.State = "OFF"
		}
	}

	d.Type = detectType(p, d)

	return d, true
}

// ParseBridgeEvent разбирает топики zigbee2mqtt/bridge/event и bridge/response/*
//
// Примеры топиков:
// zigbee2mqtt/bridge/event       				- device_joined, device_left
// zigbee2mqtt/bridge/response/permit_join		- ответ на разрешение входа
func ParseBridgeEvent(topic string, payload []byte) (BridgeEvent, bool) {
	name := strings.TrimPrefix(topic, prefix)

	if !strings.HasPrefix(name, "bridge/") {
		return BridgeEvent{}, false
	}

	p := string(payload)

	// zigbee2mqtt/bridge/event
	if name == "bridge/event" {
		eventType := gjson.Get(p, "type").String()
		model := gjson.Get(p, "data.definition.model").String()

		if eventType == EventDeviceInterview {
			status := gjson.Get(p, "data.status").String()
			if status == "successful" {
				friendly := gjson.Get(p, "data.friendly_name").String()
				ieee := gjson.Get(p, "data.ieee_address").String()
				model := gjson.Get(p, "data.definition.model").String()
				desc := strings.ToLower(gjson.Get(p, "data.definition.description").String())

				devType := devices.TypeUnknown
				switch {
				case strings.Contains(desc, "leak") || strings.Contains(desc, "water"):
					devType = devices.TypeLeak
				case strings.Contains(desc, "motion") || strings.Contains(desc, "occupancy"):
					devType = devices.TypeMotion
				case strings.Contains(desc, "door") || strings.Contains(desc, "contact"):
					devType = devices.TypeContact
				case strings.Contains(desc, "temperature") || strings.Contains(desc, "humidity"):
					devType = devices.TypeSensor
				case strings.Contains(desc, "curtain") || strings.Contains(desc, "blind"):
					devType = devices.TypeCurtain
				case strings.Contains(desc, "switch") || strings.Contains(desc, "plug") || strings.Contains(desc, "relay"):
					devType = devices.TypeRelay
				}

				log.Printf("[zigbee] interview: %s (%s) model=%s type=%s", friendly, ieee, model, devType)
				return BridgeEvent{
					Type:         "device_interview",
					FriendlyName: friendly,
					IEEE:         ieee,
					DeviceType:   devType,
					Model:        model,
				}, true
			}
		}

	}

	// zigbee2mqtt/bridge/response/permit_join
	if name == "bridge/response/permit_join" {
		status := gjson.Get(p, "status").String()
		if status == "ok" {
			return BridgeEvent{Type: EventPermitJoin}, true
		}
	}

	return BridgeEvent{}, false
}

func detectType(payload string, d devices.Device) devices.DeviceType {
	has := func(key string) bool {
		return gjson.Get(payload, key).Exists()
	}

	switch {
	case has("work_state") || has("curtain_status"):
		return devices.TypeCurtain
	case has("occupancy"):
		return devices.TypeMotion
	case has("contact"):
		return devices.TypeContact
	case has("temperature") && !has("state"):
		return devices.TypeSensor
	case has("state"):
		return devices.TypeRelay
	case has("water_leak"):
		return devices.TypeLeak
	default:
		return devices.TypeUnknown
	}
}
