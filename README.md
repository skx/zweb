# zweb

A trivial program to control zigbee devices, via direct contact with the zigbee2mqtt queue.

We server a single-page view, over HTTP, of all zigbee devices which have a friendly name matching the specified regular expression.



## Overview

Assuming you have a zigbee2mqtt deployment you already have a simple web-based front-end, but it is complex and hard to control over a table or mobile phone.

Instead this application presents just a list of all switches (matching an optional regular expression) which can be clicked to toggle their state, and otherwise updates to show "on" vs "off".

When launched it connects to the MQ host on `cube.my.flat` use the command-line flags to setup a different end-point:

```
$ ./zweb -h
Usage of ./zweb:
  -filter string
        Only display devices matching this regexp
  -http-port string
        The port to listen upon (default ":8000")
  -mq-host string
        The MQ host address (default "cube.my.flat")
  -mq-port int
        The MQ host port. (default 1883)
```



## Setup

Nothing complex:

    go build .
    ./zweb -filter="^socket-"

Then open http://127.0.0.1:8000/ in your device.



## TODO

* Have a separate section for lights, later.



## Links

* Applications
  * [zswitch](https://github.com/skx/zswitch) - Simple CLI to change state of zigbee switches.
  * [zweb](https://github.com/skx/zweb) - Simple WebUI for switch-devices.
* Library
  * [ziglib](https://github.com/skx/ziglib/) - Common library for accessing a Zigbee2mqtt rabbitmq host directly.  Used by both zswitch and zweb.
