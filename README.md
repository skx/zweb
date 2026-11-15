# zweb

A trivial program to control zigbee devices, via direct contact with the zigbee2mqtt queue.

We server a single-page view, over HTTP, of all zigbee devices which have a friendly name matching the specified regular expression.



## Overview

Assuming you have a zigbee2mqtt deployment you already have a simple web-based front-end, but it is complex and hard to control over a table or mobile phone.

Instead this application presents just a list of all switches (matching an optional regular expression) which can be clicked to toggle their state, and otherwise updates to show "on" vs "off".

When launched it connects to the MQ host on `cube.my.flat` set the environmental variable MQ_HOST to point to a different endpoint if you're not me.



## Setup

Nothing complex:

    go build .
    ./zweb -filter="^socket-"

Then open http://127.0.0.1:8000/ in your device.



## TODO

* Have a separate section for lights, later.
