# used

Is anyone using this machine? `used` watches for real (hardware) input and a
few signs of passive use — a fullscreen game, a video, a call — and reports
`active`, `present` or `unused`, plus load metrics, over webhooks, MQTT and a
local API. It never acts on the state; your scheduler does.

Scheduled work can't fool it: only physical input moves a machine out of
`unused`, and processes started through `used exec` (or registered via the
API, or matched by an ignore rule) are excluded from evidence and reported
as workload load.

Install (per user, no admin; starts at login):

```sh
curl -fsSL https://raw.githubusercontent.com/KilianSen/used/master/install.sh | sh     # macOS / Linux
irm https://raw.githubusercontent.com/KilianSen/used/master/install.ps1 | iex          # Windows
```

Update: `used update` (or `used update --check`). It fetches the latest
release, verifies its checksum, swaps the binary and restarts the daemon if
`used install` set it up. `--version v0.2.0` pins or rolls back.

Or from source:

```sh
go build -o used ./cmd/used && ./used install
./used status            # one-shot reading
./used daemon            # run: API socket + webhooks + MQTT
./used watch             # follow transitions
./used explain           # why is it in this state?
./used exec --tag job-7 -- ./render.sh
./used hold --for 2h     # I'm here, don't schedule
./used tray              # menu-bar / tray icon with hold and release
```

To publish to MQTT (and show up in Home Assistant), add to the config:

```toml
[mqtt]
broker        = "tcp://mqtt.lan:1883"
password      = "env:USED_MQTT_PASSWORD"
homeassistant = true
```

Desktop notifications (workload started here, welcome back, holds) are
`[notify] enabled = true`.

`used/<hostname>/state` then holds `active`, `present` or `unused` (retained).

Config lives at `used config` (path printed). See [DESIGN.md](DESIGN.md) for
the model, payload schema and roadmap.
