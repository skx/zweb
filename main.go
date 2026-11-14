// Present a simple Web UI of all known switches,
// allowing easy toggle via a phone.
//
// Serves on :8000
package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	mqttBroker  = "tcp://cube.local:1883"
	mqttPrefix  = "zigbee2mqtt"
	httpAddress = ":8000"

	commandTimeout = 20 * time.Second
	retryInterval  = 2 * time.Second
)

type Device struct {
	Name        string
	Description string
	State       string
}

type ZigbeeDevice struct {
	FriendlyName string `json:"friendly_name"`
	Description  string `json:"description"`
	Type         string `json:"type"`
}

var (
	mqttClient mqtt.Client

	mu      sync.RWMutex
	devices = make(map[string]Device)

	// Serialize toggles to avoid conflicting commands.
	toggleMu sync.Mutex
)

func messageHandler(_ mqtt.Client, msg mqtt.Message) {

	topic := msg.Topic()
	payload := msg.Payload()

	// Device discovery.
	if topic == mqttPrefix+"/bridge/devices" {
		var list []ZigbeeDevice

		if err := json.Unmarshal(payload, &list); err != nil {
			// log.Printf("Invalid device list: %v", err)
			return
		}

		mu.Lock()
		for _, d := range list {
			if !strings.HasPrefix(d.FriendlyName, "socket-") {
				continue
			}

			old := devices[d.FriendlyName]
			old.Name = d.FriendlyName
			old.Description = d.Description
			old.Name = d.FriendlyName
			if old.State == "" {
				old.State = "UNKNOWN"
			}
			devices[d.FriendlyName] = old
		}
		mu.Unlock()

		// Give Zigbee2MQTT a moment to finish discovery,
		// then request the current state of each device.
		go func() {
			time.Sleep(time.Second)

			for _, d := range list {
				if !strings.HasPrefix(d.FriendlyName, "socket-") {
					continue
				}

				requestDeviceState(d.FriendlyName)

				// Avoid flooding the Zigbee network.
				time.Sleep(150 * time.Millisecond)
			}
		}()

		return
	}

	// Device state updates.
	prefix := mqttPrefix + "/"
	if !strings.HasPrefix(topic, prefix) {
		return
	}

	name := strings.TrimPrefix(topic, prefix)

	// Only accept socket devices.
	if !strings.HasPrefix(name, "socket-") {
		return
	}

	// Ignore subtopics such as device/set.
	if name == "" || strings.Contains(name, "/") {
		return
	}

	var stateData struct {
		State string `json:"state"`
	}

	if err := json.Unmarshal(payload, &stateData); err != nil {
		return
	}

	state := strings.ToUpper(stateData.State)
	if state != "ON" && state != "OFF" {
		return
	}

	mu.Lock()
	d := devices[name]
	d.Name = name
	d.State = state
	devices[name] = d
	mu.Unlock()

	// log.Printf("%s = %s", name, state)
}

func getState(name string) string {
	mu.RLock()
	defer mu.RUnlock()

	return devices[name].State
}

func requestDeviceState(name string) {
	topic := mqttPrefix + "/" + name + "/get"
	payload := `{"state":""}`

	token := mqttClient.Publish(topic, 1, false, payload)

	if token.WaitTimeout(5*time.Second) && token.Error() != nil {
		log.Printf("State request failed for %s: %v", name, token.Error())
	}
}

func publishState(name, state string) error {
	payload := map[string]string{
		"state": state,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	topic := mqttPrefix + "/" + name + "/set"

	token := mqttClient.Publish(topic, 1, false, data)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("MQTT publish timed out")
	}

	return token.Error()
}

// Toggle a device and wait until MQTT confirms the new state.
// Commands are retransmitted periodically if necessary.
func toggleDevice(name string) error {
	toggleMu.Lock()
	defer toggleMu.Unlock()

	current := getState(name)

	if current != "ON" && current != "OFF" {
		return fmt.Errorf("unknown state for device %s %s", name, current)
	}

	target := "ON"
	if current == "ON" {
		target = "OFF"
	}

	log.Printf("Toggling %s: %s -> %s", name, current, target)

	deadline := time.NewTimer(commandTimeout)
	defer deadline.Stop()

	retry := time.NewTicker(retryInterval)
	defer retry.Stop()

	// Publish immediately.
	if err := publishState(name, target); err != nil {
		return err
	}

	for {
		// Check for confirmation.
		if getState(name) == target {
			log.Printf("%s successfully changed to %s", name, target)
			return nil
		}

		select {
		case <-deadline.C:
			return fmt.Errorf(
				"timeout waiting for %s to change to %s",
				name, target,
			)

		case <-retry.C:
			// Retransmit if the state hasn't changed.
			if getState(name) != target {
				// log.Printf("Retrying command for %s", name)

				if err := publishState(name, target); err != nil {
					// log.Printf("Publish error: %v", err)
				}
			}

		case <-time.After(200 * time.Millisecond):
			// Periodically check for state updates.
		}
	}
}

var pageTemplate = template.Must(template.New("index").Parse(`
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="30">
<title>Zigbee Devices</title>
<style>
	body {
		font-family: sans-serif;
		background: #181818;
		color: #eee;
		margin: 30px;
	}
	h1 {
		margin-bottom: 25px;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
		gap: 15px;
	}
	.device {
		background: #292929;
		border-radius: 12px;
		padding: 20px;
		text-align: center;
	}
	.name {
		font-size: 17px;
		margin-bottom: 15px;
		overflow-wrap: anywhere;
	}
	.state {
		font-size: 14px;
		margin-bottom: 15px;
	}
	button {
		border: 0;
		border-radius: 8px;
		padding: 12px 25px;
		font-size: 16px;
		cursor: pointer;
		width: 100%;
		color: white;
		background: #555;
	}
	button.on {
		background: #218838;
	}
	button.off {
		background: #b52b37;
	}
	button.unknown {
		background: #555;
	}
	button:hover {
		filter: brightness(1.15);
	}
</style>
</head>
<body>
<h1>Zigbee Devices</h1>

<div class="grid">
{{range .}}
	<div class="device">
	       <div class="name">{{.Description}}</div>
	       <div style="font-size:12px;color:#999;margin-bottom:12px">{{.Name}}</div>
	       <div class="state">{{.State}}</div>
		<form method="POST" action="/toggle/{{.Escaped}}">
			<button class="{{.Class}}" type="submit">
				{{if eq .State "ON"}}Turn OFF
				{{else if eq .State "OFF"}}Turn ON
				{{else}}Unknown{{end}}
			</button>
		</form>
	</div>
{{else}}
	<p>No Zigbee devices found.</p>
{{end}}
</div>
</body>
</html>
`))

type PageDevice struct {
	Name        string
	Description string
	State       string
	Escaped     string
	Class       string
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mu.RLock()

	list := make([]PageDevice, 0, len(devices))

	for _, d := range devices {
		class := "unknown"

		switch d.State {
		case "ON":
			class = "on"
		case "OFF":
			class = "off"
		}

		list = append(list, PageDevice{
			Name:        d.Name,
			Description: d.Description,
			State:       d.State,
			Escaped:     url.PathEscape(d.Name),
			Class:       class,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	mu.RUnlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := pageTemplate.Execute(w, list); err != nil {
		log.Printf("Template error: %v", err)
		os.Exit(1)
	}
}

func toggleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil || name == "" || strings.Contains(name, "/") {
		http.Error(w, "Invalid device name", http.StatusBadRequest)
		return
	}

	if getState(name) == "" {
		http.Error(w, "Unknown device", http.StatusNotFound)
		return
	}

	if err := toggleDevice(name); err != nil {
		log.Printf("Toggle failed for %s: %v", name, err)
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func main() {
	// MQTT configuration.
	opts := mqtt.NewClientOptions()
	opts.AddBroker(mqttBroker)
	opts.SetClientID("zigbee-webserver")
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		token := c.Subscribe(mqttPrefix+"/#", 1, messageHandler)
		if token.Wait() && token.Error() != nil {
			log.Printf("MQTT subscription error: %v", token.Error())
			os.Exit(1)
		}
	})

	mqttClient = mqtt.NewClient(opts)

	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("MQTT connection failed: %v", token.Error())
		os.Exit(1)
	}

	log.Printf("Connected to MQTT broker at %s", mqttBroker)

	// HTTP routes.
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/toggle/{name}", toggleHandler)

	log.Printf("HTTP server listening on %s", httpAddress)

	if err := http.ListenAndServe(httpAddress, nil); err != nil {
		// log.Fatal(err)
	}
}
