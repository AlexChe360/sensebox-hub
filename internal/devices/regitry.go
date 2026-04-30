package devices

import (
	"database/sql"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DeviceType - тип устройства, опркделяется по полям из MQTT
type DeviceType string

const (
	TypeUnknown DeviceType = "unknown"
	TypeRelay   DeviceType = "relay"   // розктка, выключатель (state ON/OFF)
	TypeCurtain DeviceType = "curtain" // шторы, жалюзи
	TypeSensor  DeviceType = "sensor"  // температура, влажность
	TypeMotion  DeviceType = "motion"  // датчик движения
	TypeContact DeviceType = "contact" // датчик открытия
	TypeLeak    DeviceType = "leak"    // датчик протечки воды
)

// Device - универсальная модель устройства.
// Поля расширяются по мере добавления новых устройств.
type Device struct {
	FriendlyName      string     `json:"friendly_name"`
	Type              DeviceType `json:"type"`
	LastSeen          time.Time  `json:"last_seen"`
	LinkQuality       int64      `json:"link_quality"`
	Battery           int64      `json:"battery"`
	State             string     `json:"state"`              // "ON" | "OFF" | "OPEN" | "CLOSE"
	DeviceTemperature float64    `json:"device_temperature"` // температура самого устройства
	PowerOutageCount  int64      `json:"power_outage_count"`
	Position          int64      `json:"position"`   // 0-100%
	WorkState         string     `json:"work_state"` // "opening" | "closing" | "stop"
	CurtainStatus     int64      `json:"curtain_state"`
	Illuminance       int64      `json:"illiminance"` // освещенность (встроенный датчик)
	TotalTime         int64      `json:"total_time"`  // время полного хода мотора
	Temperature       float64    `json:"temperature"`
	Humidity          float64    `json:"humidity"`
	Occupancy         bool       `json:"occupancy"`
	Contact           bool       `json:"contact"`
	TriggerCount      int64      `json:"trigger_count"`
}

type ChangeHandler func(old, new Device)

type Registry struct {
	mu       sync.RWMutex
	devices  map[string]Device
	db       *sql.DB
	handlers []ChangeHandler
}

func NewRegistry(dbPath string) (*Registry, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	r := &Registry{
		devices: make(map[string]Device),
		db:      db,
	}

	if err := r.migrate(); err != nil {
		return nil, err
	}

	if err := r.load(); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *Registry) OnChange(h ChangeHandler) {
	r.handlers = append(r.handlers, h)
}

func (r *Registry) Update(d Device) error {
	r.mu.Lock()
	old := r.devices[d.FriendlyName]
	d.LastSeen = time.Now()
	r.devices[d.FriendlyName] = d
	r.mu.Unlock()

	for _, h := range r.handlers {
		h(old, d)
	}

	return r.persist(d)
}

func (r *Registry) Get(name string) (Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[name]
	return d, ok
}

func (r *Registry) All() []Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		result = append(result, d)
	}
	return result
}

func (r *Registry) migrate() error {
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS devices (
			friendly_name		TEXT 	PRIMARY KEY,
			type				TEXT 	DEFAULT 'unknown',
			linkquality			INTEGER DEFAULT 0,
			battery				INTEGER DEFAULT 0,
			state				TEXT 	DEFAULT '',
			device_temperature 	REAL	DEFAULT 0,
			power_outage_count	INTEGER	DEFAULT 0,
			position			INTEGER	DEFAULT	0,
			work_state			TEXT	DEFAULT '',
			curtain_status		INTEGER	DEFAULT 0,
			illuminance			INTEGER	DEFAULT	0,
			total_time			INTEGER	DEFAULT	0,
			temperature			REAL	DEFAULT 0,
			humidity			REAL	DEFAULT 0,
			occupancy			INTEGER	DEFAULT 0,
			contact				INTEGER	DEFAULT 0,
			last_seen			DATETIME,
			trigger_count		INTEGER	DEFAULT 0
		)
	`)
	return err
}

func (r *Registry) load() error {
	rows, err := r.db.Query(`SELECT * FROM devices`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var d Device
		var occupancy, contact int
		var dtype string
		if err := rows.Scan(
			&d.FriendlyName,
			&dtype,
			&d.LinkQuality,
			&d.Battery,
			&d.State,
			&d.DeviceTemperature,
			&d.PowerOutageCount,
			&d.Position,
			&d.WorkState,
			&d.CurtainStatus,
			&d.Illuminance,
			&d.TotalTime,
			&d.Temperature,
			&d.Humidity,
			&occupancy,
			&contact,
			&d.LastSeen,
			&d.TriggerCount,
		); err != nil {
			return err
		}
		d.Type = DeviceType(dtype)
		d.Occupancy = occupancy == 1
		d.Contact = contact == 1
		r.devices[d.FriendlyName] = d
	}

	return nil
}

func (r *Registry) persist(d Device) error {
	_, err := r.db.Exec(`
		INSERT INTO devices (
			friendly_name, type, linkquality, battery,
			state, device_temperature, power_outage_count,
			position, work_state, curtain_status, illuminance, total_time,
			temperature, humidity, occupancy, contact, last_seen, trigger_count
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(friendly_name) DO UPDATE SET
		 	type				= excluded.type,
			linkquality			= excluded.linkquality,
			battery				= excluded.battery,
			state				= excluded.state,
			device_temperature	= excluded.device_temperature,
			power_outage_count	= excluded.power_outage_count,
			position			= excluded.position,
			work_state			= excluded.work_state,
			curtain_status		= excluded.curtain_status,
			illuminance			= excluded.illuminance,
			total_time			= excluded.total_time,
			temperature			= excluded.temperature,
			humidity			= excluded.humidity,
			occupancy			= excluded.occupancy,
			contact			   	= excluded.contact,
			last_seen			= excluded.last_seen,
			trigger_count		= excluded.trigger_count
	`,
		d.FriendlyName, string(d.Type), d.LinkQuality, d.Battery,
		d.State, d.DeviceTemperature, d.PowerOutageCount,
		d.Position, d.WorkState, d.CurtainStatus, d.Illuminance, d.TotalTime,
		d.Temperature, d.Humidity,
		boolToInt(d.Occupancy), boolToInt(d.Contact), d.LastSeen, d.TriggerCount,
	)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
