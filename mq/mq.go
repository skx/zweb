// Package mq provides a reusable client for a Zigbee2MQTT deployment
// backed by an MQTT broker. It owns device discovery and state tracking.
package mq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const DefaultTopicPrefix = "zigbee2mqtt"

// Config contains the connection settings for an MQTT broker.
type Config struct {
	Broker              string
	ClientID            string
	TopicPrefix         string
	ConnectTimeout      time.Duration
	RequestTimeout      time.Duration
	InitialStateTimeout time.Duration
	StateTimeout        time.Duration
	KeepAlive           time.Duration
	Username            string
	Password            string
	CleanSession        bool
}

// Device describes a device reported by Zigbee2MQTT's bridge/devices topic.
type Device struct {
	FriendlyName string `json:"friendly_name"`
	IEEEAddress  string `json:"ieee_address"`
	Description  string `json:"description"`
	Type         string `json:"type"`
	State        string `json:"-"`
}

// Client owns an MQTT connection, discovered devices, and their latest states.
type Client struct {
	client              mqtt.Client
	prefix              string
	requestLimit        time.Duration
	initialStateTimeout time.Duration
	stateTimeout        time.Duration

	mu      sync.RWMutex
	devices map[string]Device
	changed chan struct{}
}

var generatedID atomic.Uint64

// Connect connects to the broker, subscribes to Zigbee2MQTT updates, discovers
// devices, requests their current states, and waits up to InitialStateTimeout
// for devices to report. Devices which do not report before the deadline remain
// in the cache with an empty State (GetState returns "UNKNOWN").
//
// Broker must be a Paho-compatible URL, for example tcp://localhost:1883.
func Connect(config Config) (*Client, error) {
	if config.Broker == "" {
		return nil, errors.New("MQTT broker address is required")
	}
	if config.TopicPrefix == "" {
		config.TopicPrefix = DefaultTopicPrefix
	}
	config.TopicPrefix = strings.TrimSuffix(config.TopicPrefix, "/")
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 5 * time.Second
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 5 * time.Second
	}
	if config.InitialStateTimeout <= 0 {
		config.InitialStateTimeout = 5 * time.Second
	}
	if config.StateTimeout <= 0 {
		config.StateTimeout = 20 * time.Second
	}
	if config.ClientID == "" {
		config.ClientID = fmt.Sprintf(
			"zigbee2mqtt-go-%d-%d",
			time.Now().UnixNano(),
			generatedID.Add(1),
		)
	}

	c := &Client{
		prefix:              config.TopicPrefix,
		requestLimit:        config.RequestTimeout,
		initialStateTimeout: config.InitialStateTimeout,
		stateTimeout:        config.StateTimeout,
		devices:             make(map[string]Device),
		changed:             make(chan struct{}),
	}

	subscribed := make(chan error, 1)
	var initialSubscribe sync.Once

	subscribe := func(client mqtt.Client) {
		topic := c.prefix + "/#"
		token := client.Subscribe(topic, 1, c.messageHandler)

		if !token.WaitTimeout(c.requestLimit) {
			initialSubscribe.Do(func() {
				subscribed <- fmt.Errorf(
					"subscription to %q timed out", topic,
				)
			})
			return
		}

		if err := token.Error(); err != nil {
			initialSubscribe.Do(func() {
				subscribed <- fmt.Errorf(
					"subscribe to %q: %w", topic, err,
				)
			})
			return
		}

		initialSubscribe.Do(func() {
			subscribed <- nil
		})
	}

	opts := mqtt.NewClientOptions().
		AddBroker(config.Broker).
		SetClientID(config.ClientID).
		SetConnectTimeout(config.ConnectTimeout).
		SetAutoReconnect(true).
		SetOrderMatters(false).
		SetOnConnectHandler(subscribe)

	if config.KeepAlive > 0 {
		opts.SetKeepAlive(config.KeepAlive)
	}

	if config.Username != "" {
		opts.SetUsername(config.Username)
		opts.SetPassword(config.Password)
	}

	if config.CleanSession {
		opts.SetCleanSession(true)
	}

	c.client = mqtt.NewClient(opts)

	token := c.client.Connect()

	if !token.WaitTimeout(config.ConnectTimeout) {
		c.Close()
		return nil, fmt.Errorf(
			"MQTT connection to %q timed out", config.Broker,
		)
	}

	if err := token.Error(); err != nil {
		c.Close()
		return nil, fmt.Errorf(
			"MQTT connection to %q failed: %w",
			config.Broker, err,
		)
	}

	// The OnConnect callback subscribes both on the first connection and after
	// automatic reconnects. Do not begin discovery until the initial subscribe
	// has completed successfully.
	select {
	case err := <-subscribed:
		if err != nil {
			c.Close()
			return nil, err
		}
	case <-time.After(config.ConnectTimeout):
		c.Close()
		return nil, errors.New(
			"timed out waiting for MQTT topic subscription",
		)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		c.requestLimit,
	)
	defer cancel()

	devices, err := c.fetchDevices(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}

	c.mu.Lock()

	for _, device := range devices {
		if device.FriendlyName == "" {
			continue
		}

		// Preserve a state update if it arrived immediately after the
		// wildcard subscription but before the bridge device list.
		device.State = c.devices[device.FriendlyName].State
		c.devices[device.FriendlyName] = device
	}

	c.signalChangedLocked()
	c.mu.Unlock()

	// Request state for every discovered device. Publishing all requests before
	// waiting avoids adding a full timeout for each device.
	for _, device := range devices {
		if device.FriendlyName == "" ||
			strings.EqualFold(device.FriendlyName, "Coordinator") {
			continue
		}

		// Request in parallel so a slow publish for one device cannot delay
		// requests for every device that follows it.
		go func(name string) {
			_ = c.requestState(name)
		}(device.FriendlyName)
	}

	c.waitForInitialStates()

	return c, nil
}

// fetchDevices retrieves the bridge device list. The permanent wildcard
// subscription is already active before this temporary subscription is made.
func (c *Client) fetchDevices(
	ctx context.Context,
) ([]Device, error) {
	topic := c.prefix + "/bridge/devices"

	result := make(chan []Device, 1)
	decodeErr := make(chan error, 1)

	token := c.client.Subscribe(
		topic,
		1,
		func(_ mqtt.Client, msg mqtt.Message) {
			var devices []Device

			if err := json.Unmarshal(msg.Payload(), &devices); err != nil {
				select {
				case decodeErr <- fmt.Errorf(
					"decode Zigbee2MQTT device list: %w", err,
				):
				default:
				}
				return
			}

			select {
			case result <- devices:
			default:
			}
		},
	)

	if !token.WaitTimeout(c.requestLimit) {
		return nil, fmt.Errorf(
			"subscription to %q timed out", topic,
		)
	}

	if err := token.Error(); err != nil {
		return nil, fmt.Errorf(
			"subscribe to %q: %w", topic, err,
		)
	}

	defer func() {
		unsubscribe := c.client.Unsubscribe(topic)
		unsubscribe.WaitTimeout(c.requestLimit)
	}()

	timer := time.NewTimer(c.requestLimit)
	defer timer.Stop()

	select {
	case devices := <-result:
		return devices, nil

	case err := <-decodeErr:
		return nil, err

	case <-ctx.Done():
		return nil, ctx.Err()

	case <-timer.C:
		return nil, fmt.Errorf(
			"timed out waiting for Zigbee2MQTT device list on %q",
			topic,
		)
	}
}

func (c *Client) messageHandler(
	_ mqtt.Client,
	msg mqtt.Message,
) {
	prefix := c.prefix + "/"

	if !strings.HasPrefix(msg.Topic(), prefix) {
		return
	}

	name := strings.TrimPrefix(msg.Topic(), prefix)

	// State reports are published on zigbee2mqtt/<friendly_name>.
	// Ignore bridge topics and per-device subtopics such as /set and /get.
	if name == "" ||
		strings.Contains(name, "/") ||
		name == "bridge" {
		return
	}

	var payload struct {
		State string `json:"state"`
	}

	if err := json.Unmarshal(msg.Payload(), &payload); err != nil ||
		payload.State == "" {
		return
	}

	state := strings.ToUpper(payload.State)

	c.mu.Lock()

	device := c.devices[name]
	device.FriendlyName = name
	device.State = state
	c.devices[name] = device

	c.signalChangedLocked()

	c.mu.Unlock()
}

// signalChangedLocked wakes all waiters. The caller must hold c.mu.
func (c *Client) signalChangedLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Client) waitForInitialStates() {
	deadline := time.NewTimer(c.initialStateTimeout)
	defer deadline.Stop()

	for {
		c.mu.RLock()

		allKnown := true

		for name, device := range c.devices {
			if !strings.EqualFold(name, "Coordinator") &&
				device.State == "" {
				allKnown = false
				break
			}
		}

		changed := c.changed
		c.mu.RUnlock()

		if allKnown {
			return
		}

		select {
		case <-changed:
		case <-deadline.C:
			return
		}
	}
}

func (c *Client) requestState(name string) error {
	payload, err := json.Marshal(
		map[string]string{"state": ""},
	)
	if err != nil {
		return fmt.Errorf(
			"encode state request for %q: %w", name, err,
		)
	}

	token := c.client.Publish(
		c.prefix+"/"+name+"/get",
		1,
		false,
		payload,
	)

	if !token.WaitTimeout(c.requestLimit) {
		return fmt.Errorf(
			"state request for %q timed out", name,
		)
	}

	if err := token.Error(); err != nil {
		return fmt.Errorf(
			"publish state request for %q: %w", name, err,
		)
	}

	return nil
}

// Devices returns a snapshot of all discovered devices and their latest states.
func (c *Client) Devices() []Device {
	if c == nil {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	devices := make([]Device, 0, len(c.devices))

	for _, device := range c.devices {
		if device.State == "" {
			device.State = "UNKNOWN"
		}
		devices = append(devices, device)
	}

	return devices
}

// GetState returns the latest known state for a device, or "UNKNOWN" if the
// device has not reported a state (or is not in the discovered device list).
func (c *Client) GetState(name string) string {
	if c == nil {
		return "UNKNOWN"
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	device, ok := c.devices[name]

	if !ok || device.State == "" {
		return "UNKNOWN"
	}

	return device.State
}

// SetState publishes a state command and waits until the device reports that
// state. It returns an error if the command cannot be published or the device
// does not confirm the requested state before Config.StateTimeout expires.
func (c *Client) SetState(name, state string) error {
	if c == nil ||
		c.client == nil ||
		!c.client.IsConnected() {
		return errors.New("MQTT client is not connected")
	}

	name = strings.TrimSpace(name)
	state = strings.ToUpper(strings.TrimSpace(state))

	if name == "" || strings.Contains(name, "/") {
		return errors.New("invalid device name")
	}

	if state == "" {
		return errors.New(
			"desired device state cannot be empty",
		)
	}

	c.mu.RLock()
	device, exists := c.devices[name]
	c.mu.RUnlock()

	if !exists {
		return fmt.Errorf(
			"unknown Zigbee2MQTT device %q", name,
		)
	}

	if device.State == state {
		return nil
	}

	payload, err := json.Marshal(
		map[string]string{"state": state},
	)
	if err != nil {
		return fmt.Errorf(
			"encode state command: %w", err,
		)
	}

	token := c.client.Publish(
		c.prefix+"/"+name+"/set",
		1,
		false,
		payload,
	)

	if !token.WaitTimeout(c.requestLimit) {
		return fmt.Errorf(
			"publishing state command for %q timed out", name,
		)
	}

	if err := token.Error(); err != nil {
		return fmt.Errorf(
			"publish state command for %q: %w", name, err,
		)
	}

	timer := time.NewTimer(c.stateTimeout)
	defer timer.Stop()

	for {
		c.mu.RLock()

		device := c.devices[name]

		if device.State == state {
			c.mu.RUnlock()
			return nil
		}

		changed := c.changed
		c.mu.RUnlock()

		select {
		case <-changed:

		case <-timer.C:
			return fmt.Errorf(
				"timed out waiting for %q to reach state %s (last state: %s)",
				name,
				state,
				c.GetState(name),
			)
		}
	}
}

// Prefix returns the configured Zigbee2MQTT topic prefix.
func (c *Client) Prefix() string {
	return c.prefix
}

// MQTT exposes the underlying Paho client for application-specific MQTT
// operations not yet covered by this package's API.
func (c *Client) MQTT() mqtt.Client {
	return c.client
}

// Close disconnects from the broker. It is safe to call on a nil Client.
func (c *Client) Close() {
	if c != nil && c.client != nil {
		c.client.Disconnect(250)
	}
}
