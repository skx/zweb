# zweb

A trivial program to control zigbee devices, via direct contact with the zigbee2mqtt queue.

We serve a single-page UI over HTTP of all zigbee devices, optionally limiting the display to those which match a given regular expression.



## Configuration

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
    ./zweb -filter="^socket-"

Then open http://127.0.0.1:8000/ in your browser, phone, or tablet.



## TODO

* Have a separate section for lights, later.



## Links

* Applications
  * [zswitch](https://github.com/skx/zswitch) - Simple CLI to change state of zigbee switches.
  * [zweb](https://github.com/skx/zweb) - Simple WebUI for switch-devices.
* Library
  * [ziglib](https://github.com/skx/ziglib/) - Common library for accessing a Zigbee2mqtt rabbitmq host directly.  Used by both zswitch and zweb.
