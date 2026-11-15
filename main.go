// Present a simple Web UI of all known Zigbee devices,
// allowing easy toggle via a phone.
//
// Serves on :8000
package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/skx/ziglib"
)

var (
	filterRe *regexp.Regexp
	client   *ziglib.Client

	// Serialize toggles so two requests cannot toggle the same
	// device at the same time.
	toggleMu sync.Mutex
)

type PageDevice struct {
	Name        string
	Description string
	State       string
	Escaped     string
	Class       string
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
<h1>Zigbee Switches</h1>

<div class="grid">
{{range .}}
	<div class="device">
		<div class="name">{{.Description}}</div>
		<div style="font-size:12px;color:#999;margin-bottom:12px">
			{{.Name}}
		</div>
		<div class="state">{{.State}}</div>
		<form method="POST" action="/toggle/{{.Escaped}}">
			<button class="{{.Class}}" type="submit"
				{{if not (or (eq .State "ON") (eq .State "OFF"))}}disabled{{end}}>
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

<h1>Zigbee Lights</h1>
<p>Coming soon ..</p>
</body>
</html>
`))

// Return the devices to display, using the filter if one was supplied.
func pageDevices() []PageDevice {
	devices := client.Devices()

	list := make([]PageDevice, 0, len(devices))

	for _, d := range devices {
		if filterRe != nil && !filterRe.MatchString(d.FriendlyName) {
			continue
		}

		state := client.GetState(d.FriendlyName)

		class := "unknown"
		switch state {
		case "ON":
			class = "on"
		case "OFF":
			class = "off"
		}

		list = append(list, PageDevice{
			Name:        d.FriendlyName,
			Description: d.Description,
			State:       state,
			Escaped:     url.PathEscape(d.FriendlyName),
			Class:       class,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})

	return list
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := pageTemplate.Execute(w, pageDevices()); err != nil {
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

	// Check that the requested device exists and is allowed by the filter.
	found := false
	for _, d := range client.Devices() {
		if d.FriendlyName == name {
			found = true
			break
		}
	}

	if !found || (filterRe != nil && !filterRe.MatchString(name)) {
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
	// Command-line flags.
	filter := flag.String("filter", "", "Only display devices matching this regexp")
	mq_host := flag.String("mq-host", "cube.my.flat", "The MQ host address")
	mq_port := flag.Int("mq-port", 1883, "The MQ host port.")
	http_port := flag.String("http-port", ":8000", "The port to listen upon")
	flag.Parse()

	if *filter != "" {
		var err error
		filterRe, err = regexp.Compile(*filter)
		if err != nil {
			log.Fatalf("Failed to compile regexp %q: %v", *filter, err)
		}
	}

	// Connect to MQTT through the shared client package.
	var err error

	// Where we connect
	mqUri := fmt.Sprintf("tcp://%s:%d/", *mq_host, *mq_port)

	client, err = ziglib.Connect(ziglib.Config{
		Broker:   mqUri,
		ClientID: "zigbee-webserver",
	})
	if err != nil {
		log.Fatalf("MQTT connection failed: %v", err)
	}
	defer client.Close()

	log.Printf("Connected to MQTT broker at %s", mqUri)

	// Log discovered devices.
	for _, d := range client.Devices() {
		if filterRe != nil && !filterRe.MatchString(d.FriendlyName) {
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

	log.Printf("HTTP server listening on %s", *http_port)

	if err := http.ListenAndServe(*http_port, nil); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}
