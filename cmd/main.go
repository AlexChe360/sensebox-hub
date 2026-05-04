package main

import (
	"log"
	"os"
	"sensebox/internal/api"
	"sensebox/internal/automation"
	"sensebox/internal/cloude"
	"sensebox/internal/devices"
	"sensebox/internal/display"
	"sensebox/internal/mqtt"
	"sensebox/internal/zigbee"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	MQTT struct {
		Broker   string `yaml:"broker"`
		ClientID string `yaml:"client_id"`
	} `yaml:"mqtt"`
	API struct {
		Port string `yaml:"port"`
	} `yaml:"api"`
	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`
	Cloud struct {
		Enabled   bool   `yaml:"enabled"`
		ServerURL string `yaml:"server_url"`
		Serial    string `yaml:"serial"`
	} `yaml:"cloud"`
	Display struct {
		Enabled bool   `yaml:"enabled"`
		SPIPort string `yaml:"spi_port"`
		DCPin   string `yaml:"dc_pin"`
		RSTPin  string `yaml:"rst_pin"`
	} `yaml:"display"`
}

func main() {
	data, err := os.ReadFile("config/config.yaml")
	if err != nil {
		log.Fatal(err)
	}
	var cfg AppConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatal(err)
	}

	// --- Display (инициализируем первым — чтобы сразу показать setup экран) ---
	var displayMgr *display.Manager
	if cfg.Display.Enabled {
		driver, err := display.NewDriver(display.Config{
			SPIPort: cfg.Display.SPIPort,
			DCPin:   cfg.Display.DCPin,
			RSTPin:  cfg.Display.RSTPin,
		})
		if err != nil {
			log.Printf("[display] init error: %v (continuing without display)", err)
		} else {
			displayMgr = display.NewManager(driver)
			displayMgr.Start()
			// Сразу показываем setup экран — все сервисы offline
			log.Println("[display] showing setup screen")
		}
	}

	// --- Registry ---
	registry, err := devices.NewRegistry(cfg.Database.Path)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}

	// --- MQTT ---
	mqttClient := mqtt.New(cfg.MQTT.Broker, cfg.MQTT.ClientID)
	if err := mqttClient.Connect(); err != nil {
		log.Fatalf("mqtt: %v", err)
	}
	// MQTT подключился
	if displayMgr != nil {
		displayMgr.SetMQTTStatus(true)
	}

	// --- Automation Engine ---
	engine, err := automation.New("config/config.yaml", mqttClient)
	if err != nil {
		log.Fatalf("automation: %v", err)
	}
	engine.Attach(registry)

	// --- Cloud ---
	var cloudClient *cloude.Client
	if cfg.Cloud.Enabled {
		cloudClient = cloude.New(cfg.Cloud.ServerURL, cfg.Cloud.Serial, registry, mqttClient)
		cloudClient.Start()
		log.Printf("[cloud] started → %s", cfg.Cloud.ServerURL)

		// Следим за статусом облака и обновляем дисплей
		if displayMgr != nil {
			go func() {
				for {
					connected := cloudClient.IsConnected()
					displayMgr.SetCloudStatus(connected)

					code := cloudClient.PairingCode()
					if code != "" {
						displayMgr.SetPairCode(code)
					}

					time.Sleep(3 * time.Second)
				}
			}()
		}
	}

	// --- Подписка на bridge/* — следим за статусом Zigbee2MQTT ---
	mqttClient.Subscribe("zigbee2mqtt/bridge/state", func(_ string, payload []byte) {
		online := strings.Contains(string(payload), `"online"`)
		log.Printf("[zigbee] bridge state: %s", string(payload))
		if displayMgr != nil {
			displayMgr.SetZ2MStatus(online)
		}
	})

	var syncTimer *time.Timer
	var suncMu sync.Mutex
	debounceSync := func() {
		suncMu.Lock()
		defer suncMu.Unlock()
		if syncTimer != nil {
			syncTimer.Stop()
		}
		syncTimer = time.AfterFunc(5*time.Second, func() {
			if cloudClient != nil {
				cloudClient.SyncDevicesHTTP()
			}
		})
	}

	// Загружаем маппинг friendly_name → ieee из Z2M
	ieeeMap := make(map[string]string)
	var ieeeMapMu sync.Mutex
	mqttClient.Subscribe("zigbee2mqtt/bridge/devices", func(_ string, payload []byte) {
		result := gjson.ParseBytes(payload)
		result.ForEach(func(_, dev gjson.Result) bool {
			fn := dev.Get("friendly_name").String()
			ieee := dev.Get("ieee_address").String()
			model := dev.Get("definition.model").String()
			if fn == "" {
				return true
			}
			ieeeMapMu.Lock()
			if ieee != "" {
				ieeeMap[fn] = ieee
			}
			ieeeMapMu.Unlock()
			if d, ok := registry.Get(fn); ok {
				changed := false
				if model != "" && d.Model == "" {
					d.Model = model
					changed = true
				}
				if ieee != "" && d.IEEE == "" {
					d.IEEE = ieee
					changed = true
				}
				if changed {
					registry.Update(d)
					log.Printf("[zigbee] enriched %s: model=%s ieee=%s", fn, model, ieee)
				}
			}
			return true
		})
		ieeeMapMu.Unlock()
	})

	// --- Подписка на все топики ---
	mqttClient.Subscribe("zigbee2mqtt/#", func(topic string, payload []byte) {

		// Bridge события
		if event, ok := zigbee.ParseBridgeEvent(topic, payload); ok {
			switch event.Type {

			case zigbee.EventDeviceJoined:
				log.Printf("[zigbee] device joined: %s (%s)", event.FriendlyName, event.IEEE)
				registry.Update(devices.Device{
					FriendlyName: event.FriendlyName,
					IEEE:         event.IEEE,
					Type:         devices.TypeUnknown,
				})
				if cloudClient != nil {
					cloudClient.NotifyDeviceJoined(event.FriendlyName, event.IEEE)
					go cloudClient.SyncDevicesHTTP()
				}
				if displayMgr != nil {
					displayMgr.ShowPairingScreen(30 * time.Second)
				}

			case zigbee.EventDeviceLeft:
				log.Printf("[zigbee] device left: %s (%s)", event.FriendlyName, event.IEEE)
				if cloudClient != nil {
					cloudClient.NotifyDeviceLeft(event.FriendlyName, event.IEEE)
				}

			case zigbee.EventPermitJoin:
				log.Println("[zigbee] permit_join confirmed")
				if displayMgr != nil {
					displayMgr.ShowPairingScreen(120 * time.Second)
				}
			case zigbee.EventDeviceInterview:
				log.Printf("[zigbee] interview complete: %s (%s) type=%s", event.FriendlyName, event.IEEE, event.DeviceType)
				if d, ok := registry.Get(event.FriendlyName); ok {
					d.Type = event.DeviceType
					d.IEEE = event.IEEE
					d.Model = event.Model
					registry.Update(d)
				}
			}
			return
		}

		// Обычное сообщение от устройства
		device, ok := zigbee.ParseMessage(topic, payload)
		if !ok {
			return
		}
		// Подставляем IEEE из маппинга
		if device.IEEE == "" {
			ieeeMapMu.Lock()
			if ieee, ok := ieeeMap[device.FriendlyName]; ok {
				device.IEEE = ieee
			}
			ieeeMapMu.Unlock()
		}
		// Или из существующего registry
		if device.IEEE == "" {
			if existing, ok := registry.Get(device.FriendlyName); ok && existing.IEEE != "" {
				device.IEEE = existing.IEEE
			}
		}
		if err := registry.Update(device); err != nil {
			log.Printf("[registry] update error: %v", err)
		}
		if displayMgr != nil {
			displayMgr.SetDeviceCount(len(registry.All()))
		}
		debounceSync()
	})

	// --- REST API (блокирует main) ---
	log.Printf("[api] listening on :%s", cfg.API.Port)
	if err := api.New(registry, mqttClient).Run(":" + cfg.API.Port); err != nil {
		log.Fatal(err)
	}
}
