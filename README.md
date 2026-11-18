# zweb

A trivial program to control zigbee devices, via direct contact with the zigbee2mqtt queue.

We serve a single-page UI over HTTP of all zigbee devices, arranged into groups.



## Configuration

Launch the server specifying the path to a configuration file, defining your group-names and members.  Group members are matched by regular expression on their friendly-names.

Here's my example:

```json
{
  "groups": [
    {
      "name": "Switches",
      "pattern": "^socket-"
    },
    {
      "name": "Lights",
      "pattern": "^lightbulb-"
    }
  ]
}
```

Any device which doesn't match a pattern in any group is ignored.



## MQ Configuration

By default we connect to the MQ instance on `cube.my.flat` but command-line switches can configure the endpoint-details:

| Flag       |                                    Purpose |
| ---------- | ------------------------------------------ |
| `-mq-host` | Specify the hostname or IP of the queue.   |
| `-mq-user` | Specify the username to connect with.      |
| `-mq-pass` | Specify the password to connect with.      |
| `-mq-port` | Specify the port-number to contact.        |



## Setup

Nothing complex:

    go build .
    ./zweb [flags] -config=/path/to/devices.json

Then open http://127.0.0.1:8000/ in your browser, phone, or tablet.



## TODO

* It might be nice to support more than on/off toggling, especially in the case of RGB lightbulbs.
* New devices will appear as they are paired.
  * _However_ their descriptions will not.  You need to restart for that.

## Links

* Applications
  * [zalert](https://github.com/skx/zalert) - Signal an alert by flashing bulbs which are turned on.
  * [zswitch](https://github.com/skx/zswitch) - Simple CLI to change state of zigbee switches.
  * [zweb](https://github.com/skx/zweb) - Simple WebUI for switch-devices.
* Library
  * [ziglib](https://github.com/skx/ziglib/) - Common library for accessing a Zigbee2mqtt rabbitmq host directly.  Used by both zswitch and zweb.
