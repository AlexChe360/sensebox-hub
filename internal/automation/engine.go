package automation

import (
	"fmt"
	"log"
	"os"
	"sensebox/internal/devices"
	"sensebox/internal/mqtt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Automations []Rule `yaml:"automations"`
}

type Rule struct {
	Name    string  `yaml:"name"`
	Trigger Trigger `yaml:"trigger"`
	Action  Action  `yaml:"action"`
}

type Trigger struct {
	Device   string `yaml:"device"`
	Field    string `yaml:"field"`
	Operator string `yaml:"operator"`
	Value    any    `yaml:"value"`
}

type Action struct {
	Type    string `yaml:"type"` // mqtt | log
	Topic   string `yaml:"topic"`
	Payload string `yaml:"payload"`
	Message string `yaml:"message"`
}

type Engine struct {
	rules []Rule
	mqtt  *mqtt.Client
}

func New(configPath string, mqttClient *mqtt.Client) (*Engine, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	log.Printf("[automation] loaded %d rules", len(cfg.Automations))
	return &Engine{rules: cfg.Automations, mqtt: mqttClient}, nil
}

func (e *Engine) Attach(r *devices.Registry) {
	r.OnChange(func(old, new devices.Device) {
		for _, rule := range e.rules {
			if rule.Trigger.Device != new.FriendlyName {
				continue
			}
			if e.triggered(rule.Trigger, old, new) {
				e.execute(rule, new)
			}
		}
	})
}

func (e *Engine) triggered(t Trigger, old, new devices.Device) bool {
	newVal := fieldValue(new, t.Field)
	oldVal := fieldValue(old, t.Field)

	op := t.Operator
	if op == "" {
		op = "eq"
	}

	switch op {
	case "eq":
		return toString(newVal) == toString(t.Value) &&
			toString(oldVal) != toString(t.Value)
	case "neq":
		return toString(newVal) != toString(t.Value)
	case "lt":
		return toFloat(newVal) < toFloat(t.Value)
	case "gt":
		return toFloat(newVal) > toFloat(t.Value)
	case "contains":
		return strings.Contains(toString(newVal), toString(t.Value))
	}
	return false
}

func (e *Engine) execute(rule Rule, d devices.Device) {
	log.Printf("[automation] triggered: %s", rule.Name)

	switch rule.Action.Type {
	case "mqtt":
		e.mqtt.Publish(rule.Action.Topic, rule.Action.Payload)
		log.Printf("[automation] -> %s : %s", rule.Action.Topic, rule.Action.Payload)

	case "log":
		msg := rule.Action.Message
		msg = strings.ReplaceAll(msg, "{device}", d.FriendlyName)
		msg = strings.ReplaceAll(msg, "{value}", fmt.Sprintf("%v", fieldValue(d, rule.Trigger.Field)))
		msg = strings.ReplaceAll(msg, "{state}", d.State)
		msg = strings.ReplaceAll(msg, "{position}", fmt.Sprintf("%d", d.Position))
		log.Printf("[automation] %s", msg)
	}
}

// fieldValue возаращает значение поля устройства по имени.
// Поддерживает все поля Device - добавляй новые по мере роста.
func fieldValue(d devices.Device, field string) any {
	switch field {
	case "state":
		return d.State
	case "battery":
		return float64(d.Battery)
	case "link_quality", "linkquality":
		return float64(d.LinkQuality)
	case "device_temperature":
		return d.DeviceTemperature
	case "power_outage_count":
		return float64(d.PowerOutageCount)
	case "position":
		return float64(d.Position)
	case "work_state":
		return d.WorkState
	case "illuminance":
		return float64(d.Illuminance)
	case "temperature":
		return d.Temperature
	case "himidity":
		return d.Humidity
	case "occupancy":
		return d.Occupancy
	case "contact":
		return d.Contact
	}
	return nil
}

func toString(v any) string { return fmt.Sprintf("%v", v) }

func toFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	}
	return 0
}
