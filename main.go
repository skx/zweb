package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skx/ziglib"
)

//go:embed index.tmpl
var templateFS embed.FS

var pageTemplate = template.Must(
	template.ParseFS(templateFS, "index.tmpl"),
)

var (
	client *ziglib.Client
	groups []DeviceGroup

	// Serialize toggles so two requests cannot toggle the same
	// device at the same time.
	toggleMu sync.Mutex
)

// Configuration structures.

type Config struct {
	Groups []GroupConfig `json:"groups"`
}

type GroupConfig struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

// DeviceGroup contains a configured group and its compiled regexp.
type DeviceGroup struct {
	Name    string
	Pattern *regexp.Regexp
}

// Template structures.

type PageDevice struct {
	Name        string
	Description string
	State       string
	Escaped     string
	Class       string
}

type PageGroup struct {
	Name    string
	Devices []PageDevice
}

type PageData struct {
	Groups []PageGroup
}

// Load and validate the configuration file.
func loadConfig(filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("reading config file: %w", err)
	}

	var config Config

	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	if len(config.Groups) == 0 {
		return fmt.Errorf("configuration contains no groups")
	}

	groups = make([]DeviceGroup, 0, len(config.Groups))

	for _, g := range config.Groups {
		if strings.TrimSpace(g.Name) == "" {
			return fmt.Errorf("group name cannot be empty")
		}

		if g.Pattern == "" {
			return fmt.Errorf("pattern cannot be empty for group %q", g.Name)
		}

		re, err := regexp.Compile(g.Pattern)
		if err != nil {
			return fmt.Errorf(
				"invalid pattern %q for group %q: %w",
				g.Pattern, g.Name, err,
			)
		}

		groups = append(groups, DeviceGroup{
			Name:    g.Name,
			Pattern: re,
		})
	}

	return nil
}

// Return true if a device matches at least one configured group.
func allowedDevice(name string) bool {
	for _, g := range groups {
		if g.Pattern.MatchString(name) {
			return true
		}
	}

	return false
}

// Return the devices grouped according to the configuration.
func pageGroups() PageData {
	devices := client.Devices()

	result := PageData{
		Groups: make([]PageGroup, 0, len(groups)),
	}

	for _, g := range groups {
		group := PageGroup{
			Name:    g.Name,
			Devices: make([]PageDevice, 0),
		}

		for _, d := range devices {
			if !g.Pattern.MatchString(d.FriendlyName) {
				continue
			}

			state := d.State

			class := "unknown"

			switch state {
			case "ON":
				class = "on"
			case "OFF":
				class = "off"
			}

			group.Devices = append(group.Devices, PageDevice{
				Name:        d.FriendlyName,
				Description: d.Description,
				State:       state,
				Escaped:     url.PathEscape(d.FriendlyName),
				Class:       class,
			})
		}

		sort.Slice(group.Devices, func(i, j int) bool {
			return group.Devices[i].Name < group.Devices[j].Name
		})

		result.Groups = append(result.Groups, group)
	}

	sort.Slice(result.Groups, func(i, j int) bool {
		return result.Groups[i].Name < result.Groups[j].Name
	})

	return result
}

// HTTP handlers.

// Server root shows all groups.
func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := pageTemplate.Execute(w, pageGroups()); err != nil {
		log.Printf("Template error: %v", err)
	}
}

// Toggle a device and wait for the MQTT client to confirm the new state.
func toggleDevice(name string) error {
	toggleMu.Lock()
	defer toggleMu.Unlock()

	current := client.GetState(name)

	var target string

	switch current {
	case "ON":
		target = "OFF"
	case "OFF":
		target = "ON"
	default:
		return fmt.Errorf("unknown state for device %s: %s", name, current)
	}

	log.Printf("Toggling %s: %s -> %s", name, current, target)

	if err := client.SetState(name, target); err != nil {
		return fmt.Errorf("failed to set %s to %s: %w", name, target, err)
	}

	log.Printf("%s successfully changed to %s", name, target)
	return nil
}

func toggleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := r.PathValue("name")

	if name == "" || strings.Contains(name, "/") {
		http.Error(w, "Invalid device name", http.StatusBadRequest)
		return
	}

	// Check that the requested device exists and belongs to a configured group.
	found := false

	for _, d := range client.Devices() {
		if d.FriendlyName == name {
			found = true
			break
		}
	}

	if !found || !allowedDevice(name) {
		http.Error(w, "Unknown device", http.StatusNotFound)
		return
	}

	if err := toggleDevice(name); err != nil {
		log.Printf("Toggle failed for %s: %v", name, err)
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}

	// Redirect to "/#name" so that the newly-changed ID gets focus.
	http.Redirect(w, r, "/#"+name, http.StatusSeeOther)
}

// Connect to MQTT, retrying a bunch of times if we see failure.
func connectMQTT(config ziglib.Config) (*ziglib.Client, error) {
	delay := 2 * time.Second
	count := 1
	max := 10
	for count < max {
		log.Printf("Connecting to MQTT broker attempt %d/%d...", count, max)

		c, err := ziglib.Connect(config)
		if err == nil {
			return c, nil
		}

		log.Printf("MQTT connection failed: %v", err)
		log.Printf("Retrying in %s...", delay)

		time.Sleep(delay)

		count++
	}
	return nil, fmt.Errorf("failed to connect to MQ after %d attempts", count)
}

// Main.
func main() {
	// Command-line flags.
	configFile := flag.String("config", "devices.json", "Device group configuration file")
	mqHost := flag.String("mq-host", "cube.my.flat", "The MQ host address")
	mqPort := flag.Int("mq-port", 1883, "The MQ host port")
	mqUser := flag.String("mq-user", "", "The username to authenticate to the queue with")
	mqPass := flag.String("mq-pass", "", "The password to authenticate to the queue with")
	httpPort := flag.String("http-port", ":8000", "The port to listen upon")

	flag.Parse()

	// Load device group configuration.
	if err := loadConfig(*configFile); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	log.Printf("Loaded %d device groups from %s", len(groups), *configFile)

	for _, g := range groups {
		log.Printf("Group: %-20s Pattern: %s", g.Name, g.Pattern.String())
	}

	// Connect to MQTT through the shared client package.
	mqUri := fmt.Sprintf("tcp://%s:%d/", *mqHost, *mqPort)

	var err error

	client, err = connectMQTT(ziglib.Config{
		Broker:   mqUri,
		ClientID: "zigbee-webserver",
		Username: *mqUser,
		Password: *mqPass,
	})

	if err != nil {
		log.Fatalf("MQTT connection failed: %v", err)
	}

	defer client.Close()

	log.Printf("Connected to MQTT broker at %s", mqUri)

	// Log discovered devices belonging to configured groups.
	for _, d := range client.Devices() {
		if !allowedDevice(d.FriendlyName) {
			continue
		}

		log.Printf(
			"Device: %-25s State: %-7s Description: %s",
			d.FriendlyName,
			client.GetState(d.FriendlyName),
			d.Description,
		)
	}

	// HTTP routes.
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/toggle/{name}", toggleHandler)

	log.Printf("HTTP server listening on %s", *httpPort)

	if err := http.ListenAndServe(*httpPort, nil); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}
