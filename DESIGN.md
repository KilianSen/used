# used — design

> Status: draft · 2026-10-27 · implementation in progress (see §13)

`used` is a single cross-platform binary that answers one question continuously:
**is a human using this machine right now?** — and reports that, together with
performance metrics, to whoever subscribes (webhooks, local API, CLI).

It **detects and reports only**. It never starts, pauses, throttles or kills
anything. Whatever consumes its output (a k8s-like scheduler, a render farm,
a home-automation hub) decides what to do.

---

## 1. Goals and non-goals

**Goals**

- Reliable presence detection on macOS, Windows and Linux (X11 + Wayland).
- Distinguish *"no input but still in use"* (game open, user on a short break,
  watching a video, in a call) from *"user has left"*.
- Stay correct when the consumer **schedules work onto the machine** — a
  scheduled fullscreen 3D or GPU job must never make the machine look "used".
- Scheduler independent: integration via process wrapper, local API, or
  static rules — no assumptions about the orchestrator.
- Performance metrics (CPU, memory, disk, network, GPU, thermals, power),
  split into *user* vs *workload* load.
- Push via signed webhooks; pull via local API and CLI.
- One binary, one install command, no runtime dependencies.

**Non-goals**

- Acting on the state (scheduling, job control, power management).
- Capturing input content, screen content, window titles or URLs.
- Fleet coordination. Every machine reports independently; aggregation is
  the receiver's job.
- Requiring a dedicated OS account for workloads.

---

## 2. Presence model

### 2.1 States

| State     | Meaning                                                                  |
|-----------|--------------------------------------------------------------------------|
| `active`  | Recent hardware input from a local human.                                |
| `present` | No recent input, but supporting evidence that the session is still in use. |
| `unused`  | No input and no (non-ignored) evidence, or the session is locked/away.   |

Plus an orthogonal `override` field (`none` / `hold` / `release`, see §2.6).

### 2.2 The latch rule (core invariant)

> **Only hardware input can move the machine out of `unused`.**
> Supporting evidence can *keep* a machine in `present`, never *bring* it there.

```
            hardware input
 unused ───────────────────────▶ active
   ▲                              │ no input for idle_after
   │                              ▼
   │  evidence gone, or      present ◀── evidence (fullscreen, audio,
   │  present_max reached,        │       assertions, call, …)
   │  or lock / session switch    │
   └──────────────────────────────┘
   lock / session switch from any state ──▶ unused
```

Consequence: a workload started on an `unused` machine — however fullscreen,
loud or GPU-heavy — cannot flip it back. This removes the scheduler feedback
loop by construction, *even if the workload was never registered as ignored*.
Ignore rules (§4) are still needed so that a workload started while the
user is `active`/`present` does not extend `present` after the user leaves.

### 2.3 Hardware input

Input must originate from a physical (or explicitly trusted) device.
Synthetic input (automation, anti-AFK tools, `SendInput`, XTEST) must not count.

| OS      | Primary source                                                                 | Synthetic input filtered? | Fallback |
|---------|--------------------------------------------------------------------------------|---------------------------|----------|
| macOS   | `CGEventSourceSecondsSinceLastEventType(kCGEventSourceStateHIDSystemState, …)`, IOKit `HIDIdleTime` | Yes (HID level)          | — |
| Windows | Low-level keyboard/mouse hooks in the session agent; drop events with `LLKHF_INJECTED` / `LLMHF_INJECTED` | Yes                      | `GetLastInputInfo` (counts injected input → `input_source: "os_idle"`, lower confidence) |
| Linux   | Opt-in: libinput / `/dev/input/event*` (requires `input` group)                | Yes                       | logind `IdleHint` → Mutter `IdleMonitor` (GNOME) → `ext-idle-notify-v1` (wlroots/KDE) → XScreenSaver (X11; counts XTEST) |

Every report carries `input_source` so receivers can weigh trust.

**Remote input** (RDP, VNC, screen sharing) is classified separately as
`input_source: "remote"` and is configurable (`count_remote_input`, default
`false`). A remote-desktop session driven by a scheduler would otherwise look
like a human. Detection: Windows WTS session protocol; Linux logind session
`Remote`; macOS screen-sharing process / `ARDAgent` presence (best effort).

### 2.4 Supporting evidence

Evaluated only while in `active`/`present`. Each signal is attributed to a PID
wherever the OS allows it, so ignore rules (§4) can filter it.

| Signal                    | macOS                                          | Windows                                         | Linux                                         | PID attribution |
|---------------------------|------------------------------------------------|-------------------------------------------------|-----------------------------------------------|-----------------|
| Fullscreen / exclusive 3D | Frontmost app + window bounds vs screen (`CGWindowListCopyWindowInfo`, owner only — no title, no Screen Recording permission) | `SHQueryUserNotificationState` (`QUNS_RUNNING_D3D_FULL_SCREEN`, `QUNS_PRESENTATION_MODE`), `GetForegroundWindow` | X11 `_NET_ACTIVE_WINDOW` + `_NET_WM_STATE_FULLSCREEN`; Wayland: not available generically | yes / yes / X11 only |
| Audio playback            | CoreAudio process objects (`kAudioHardwarePropertyProcessObjectList`, `IsRunningOutput`) | WASAPI audio sessions + peak meter              | PipeWire nodes (`application.process.id`)      | yes |
| Camera / mic in use       | CoreMediaIO / CoreAudio `IsRunningInput`       | CapabilityAccessManager consent store (`LastUsedTimeStop == 0`) | PipeWire input nodes, `/dev/video*` holders | yes (mostly) |
| Display/sleep assertions  | IOPMAssertions (`PreventUserIdleDisplaySleep`) | `PowerRequests` (`CallNtPowerInformation`)       | logind inhibitors (`idle`)                     | yes |
| Foreground GPU use        | coarse only                                    | `GPU Engine` perf counters (PID in instance)     | DRM `fdinfo`, NVML                              | yes (macOS coarse) |
| Allow-listed app running  | config                                         | config                                          | config                                         | yes |

Unattributable evidence (e.g. Wayland fullscreen without PID) is reported but
weighted lower and can be disabled per signal.

### 2.5 Timers and hysteresis

| Setting         | Default | Meaning                                                         |
|-----------------|---------|-----------------------------------------------------------------|
| `idle_after`    | `2m`    | `active` → `present`/`unused` after this long without input.    |
| `unused_after`  | `10m`   | `present` without evidence → `unused`.                          |
| `present_max`   | `45m`   | Hard cap: evidence alone cannot hold `present` longer than this. Prevents a game left open overnight counting as used. |
| `evidence_hold` | `30s`   | Evidence keeps counting this long after it was last seen (anti-flap: a gap between songs or a window switch doesn't drop `present`). |

Lock screen, session switch, display sleep and system suspend go straight to
`unused`. Hardware input always goes straight to `active`.

`unused_after` also acts as a grace period: for that long after the last
input the machine is `present` even without evidence (a short break).

### 2.6 Manual overrides

- `used hold [--for 2h]` — force `present` ("I'm here, don't schedule").
- `used release [--for 1h]` — force `unused` ("I'm leaving, go ahead").
  Hardware input during a release ends it unless `--sticky`.

Overrides are reported (`override` field), never silently merged.

### 2.7 Confidence

Each state comes with `confidence ∈ [0,1]` and `reasons[]`. Confidence drops for
fallback input sources, unattributable evidence, and missing probes (e.g.
headless Linux). Receivers can apply their own threshold.

### 2.8 Presets

| Preset     | Behaviour                                                                     |
|------------|-------------------------------------------------------------------------------|
| `strict`   | Hardware input only. No `present` state; `idle_after` → `unused`.            |
| `balanced` | Default. All attributable evidence, `present_max = 45m`.                     |
| `lenient`  | Also unattributable evidence, `present_max = 2h`.                            |

Any preset can be refined per signal in config.

---

## 3. Architecture

One binary, two roles:

```
┌────────────────────────── used (single binary) ──────────────────────────┐
│                                                                          │
│  used daemon   (system service: root / LocalSystem / launchd daemon)     │
│   ├─ metrics       CPU, mem, disk, net, GPU, thermals, power, per-PID    │
│   ├─ workloads     ignore registry: wrappers, API leases, static rules   │
│   ├─ presence      state machine, merges agent reports per session       │
│   ├─ outputs       webhooks (queue, retry, signing), local API, metrics  │
│   └─ store         small on-disk state (bbolt): queue, leases, seq       │
│                                                                          │
│  used agent    (one per interactive session; launched at login)          │
│   └─ probes        input, lock, fullscreen, audio, camera/mic, overrides │
│                    → streams to daemon over local IPC                     │
│                                                                          │
│  used exec / status / watch / explain / hold / release / install         │
└──────────────────────────────────────────────────────────────────────────┘
```

Why split:

- Workloads (k8s-like pods, containers) usually run as **other users / root**.
  Per-process metrics and process-tree tracking across users need system
  privileges.
- Input, fullscreen and audio are only visible **inside the user's session**
  (Windows session 0 isolation; macOS per-session WindowServer; Wayland
  compositor sockets).

Multi-session machines (fast user switching, terminal servers): the daemon
tracks one presence state per session and derives a machine state
(`active` > `present` > `unused` — any session in use means the machine is
used). Both are reported.

Headless machines (no agent ever connects): presence derives from logind
sessions / SSH / console TTY input; reported with low confidence and
`agent: "none"`.

IPC: Unix domain socket (`/run/used/used.sock`, macOS
`/var/run/used/used.sock`) or Windows named pipe (`\\.\pipe\used`). Peer
identity via `SO_PEERCRED` / `LOCAL_PEERPID` + `getpeereid` /
`GetNamedPipeClientProcessId`.

---

## 4. Workload exclusion (scheduler independent)

The consumer must be able to say *"this is mine, not the user's"*. `used`
supports three mechanisms; all are scheduler agnostic.

### 4.1 `used exec` wrapper

```
used exec --tag render-42 [--ttl 6h] [--meta k=v] -- blender -b scene.blend
```

- Registers itself with the daemon **before** exec'ing the child (no race).
- Tracks the whole process tree:
  - Linux: moves itself into a dedicated cgroup v2 (`used.slice/<tag>`) when
    permitted, otherwise registers its own cgroup + PID tree.
  - Windows: Job Object with `JOB_OBJECT_LIMIT_BREAKAWAY_OK` *not* set, so
    children cannot escape.
  - macOS: process group + periodic `proc_listchildpids` walk (no cgroup
    equivalent; reparented orphans are caught via `pbi_ppid` history).
- Lease is tied to the wrapper's IPC connection: when the wrapper exits or
  dies, the lease ends (after a short drain so trailing children are still
  attributed).
- Exit code, signals and stdio pass through transparently.

**Inside containers**: mount the daemon socket and the `used` binary (or a
tiny static `used-exec` build) into the container and use it as the entrypoint
prefix. The daemon resolves the caller via `SO_PEERCRED`, which yields the PID
in the **host** PID namespace, then ignores that PID's cgroup/tree. No
container-runtime integration needed.

### 4.2 Local API leases

For schedulers that spawn processes themselves:

```
POST /v1/workloads
{ "tag": "render-42",
  "match": { "pid": 1234, "tree": true }          // or
           { "cgroup": "/kubepods.slice/…/cri-containerd-abc.scope" }  // Linux
           { "job_object": "Global\\farm-render-42" }                 // Windows
  "ttl": "6h",
  "meta": { "pod": "render-42", "ns": "farm" } }

→ 201 { "id": "wl_01J…", "expires_at": "…" }

PUT    /v1/workloads/{id}/renew
DELETE /v1/workloads/{id}
```

Leases always expire (`ttl` required, max configurable). Renewal is explicit.

### 4.3 Static rules

For long-lived infrastructure that should always be excluded:

```toml
[[ignore]]
cgroup = "/kubepods.slice/**"          # Linux: everything a kubelet-like agent starts

[[ignore]]
path      = "/opt/farm/bin/*"
publisher = "Example Farm GmbH"        # verified code signature (Authenticode / codesign)

[[ignore]]
parent = "containerd-shim*"            # any descendant of a matching process
```

Matchers: `cgroup` (glob), `path` (glob), `name` (glob), `publisher`
(verified signature), `parent` (ancestor glob), `job_object` (Windows).
Name-only rules are allowed but flagged in `used explain` as spoofable.

### 4.4 Semantics

- Ignored processes are removed from **supporting evidence** (fullscreen,
  audio, camera/mic, assertions, GPU) and their resources go to the
  `workload` metrics bucket.
- **Ignore rules never filter hardware input.** A process cannot hide a human
  by registering itself. Worst case of a malicious registration: that
  process's evidence and load are attributed to the workload bucket.
- Every ignore decision is visible in `used explain` and (by tag) in reports.

### 4.5 k8s-like integration patterns

| Setup                                  | Recommended mechanism                                   |
|----------------------------------------|---------------------------------------------------------|
| Linux node, containerd/CRI-O           | Static `cgroup = "/kubepods.slice/**"` rule (zero changes to workloads). Optionally per-pod API leases for tags/metadata. |
| Linux, custom node agent spawning processes | `used exec` or API lease with `cgroup`/`pid`.          |
| Windows node                           | Node agent creates a Job Object per workload and registers it (`job_object`), or wraps with `used exec`. |
| macOS node                             | `used exec` (process tree) — no cgroups.                |
| Any, workload driven via remote desktop | Keep `count_remote_input = false`.                    |

---

## 5. Metrics

Collected by the daemon every `metrics.interval` (default `5s`), reported as
latest + short window aggregates (avg / p95 over the heartbeat interval).

| Group    | Fields                                                                 | Source |
|----------|------------------------------------------------------------------------|--------|
| CPU      | util %, per-core optional, load avg, frequency                         | gopsutil |
| Memory   | used, available, swap, pressure (PSI on Linux, memory pressure on macOS) | gopsutil + OS |
| Disk     | read/write bytes/s, free space per mount                               | gopsutil |
| Network  | rx/tx bytes/s per interface (aggregate by default)                     | gopsutil |
| GPU      | util %, VRAM used/total, temp, power, per-PID util                     | NVML (dlopen via purego), Windows `GPU Engine`/`GPU Adapter Memory` counters, Linux DRM `fdinfo` + sysfs, macOS IOAccelerator `PerformanceStatistics` |
| Thermal  | CPU/GPU temps, throttling state                                        | gopsutil sensors, IOKit thermal state, WMI |
| Power    | AC/battery, battery %, lid, power draw where available                 | IOPowerSources, `GetSystemPowerStatus`, UPower/sysfs |

Every resource metric is reported three ways:

```json
"metrics": {
  "total":    { "cpu": 92.1, "mem_used": 0.71, "gpu": [{ "index": 0, "util": 97, "vram_used": 0.80 }] },
  "user":     { "cpu":  3.9, "mem_used": 0.22, "gpu": [{ "index": 0, "util":  1, "vram_used": 0.05 }] },
  "workload": { "cpu": 88.2, "mem_used": 0.49, "gpu": [{ "index": 0, "util": 96, "vram_used": 0.75 }],
                "by_tag": { "render-42": { "cpu": 88.2, "gpu_util": 96 } } },
  "attribution": { "gpu": "exact" }      // "exact" | "approximate" | "none"
}
```

`user` = total − workload − system baseline (kernel, `used` itself).
macOS GPU attribution is `approximate`.

---

## 6. Outputs

### 6.1 Events

| Event               | When                                                    |
|---------------------|---------------------------------------------------------|
| `state.changed`     | Machine or session presence state changes (immediate).  |
| `heartbeat`         | Every `interval` (default `60s`): full state + metrics. |
| `override.changed`  | `hold` / `release` set or cleared.                      |
| `workload.started` / `workload.ended` | Lease created / ended (opt-in).        |
| `daemon.started`    | Daemon came up (seq restarts at 1).                     |
| `agent.lost`        | Session agent disconnected unexpectedly.               |

### 6.2 Payload (v1)

```json
{
  "version": 1,
  "event": "state.changed",
  "id": "evt_01J…",
  "seq": 1042,
  "ts": "2026-10-27T18:40:00Z",
  "machine": { "id": "a3f9…", "hostname": "ws-17", "os": "windows", "arch": "amd64", "labels": { "site": "office-a" } },
  "presence": {
    "state": "present",
    "previous": "active",
    "since": "2026-10-27T18:39:12Z",
    "idle_seconds": 312,
    "input_source": "hid",
    "confidence": 0.85,
    "reasons": ["fullscreen_3d", "audio_output"],
    "override": "none",
    "sessions": [{ "id": "2", "user_hash": "…", "state": "present", "remote": false }]
  },
  "workloads": [{ "tag": "render-42", "since": "…" }],
  "metrics": { "…": "see §5" }
}
```

- `machine.id`: stable random ID generated at install (not hardware serials).
- `user_hash`: salted hash of the username; plain usernames are opt-in.
- App names in `reasons` (e.g. `fullscreen_3d:steam.exe`) are **opt-in**
  (`report.app_names = true`).
- The schema is versioned; additive changes only within a major version. A
  JSON Schema file ships with each release.

### 6.3 Webhook delivery

- Multiple endpoints, each with its own event filter, interval and secret.
- `POST`, `Content-Type: application/json`.
- Signature: `X-Used-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`;
  receivers reject if `|now − t| > 5m` (replay protection).
- `X-Used-Event`, `X-Used-Delivery` (event id, for idempotency).
- At-least-once: exponential backoff (1s → 5m, jitter), persistent on-disk
  queue (bounded, default 10 000 events / 50 MB, oldest heartbeats dropped
  first, state changes kept). In-order per endpoint; `seq` lets receivers
  detect gaps.
- Optional mTLS client certificate; custom headers (e.g. bearer token).

### 6.4 MQTT

One broker per machine (`[mqtt]`), for Home Assistant, Node-RED or
schedulers that already speak MQTT. Topics under a prefix (default
`used/<hostname>`):

| Topic               | Retained | Payload                                       |
|---------------------|----------|-----------------------------------------------|
| `…/availability`    | yes      | `online` / `offline` (offline is the last will) |
| `…/state`           | yes      | `active` / `present` / `unused`               |
| `…/status`          | yes      | latest event payload (§6.2)                   |
| `…/event/<type>`    | no       | every event, filtered by `events`             |

- On every (re)connect: `online`, discovery, then the latest status, so
  retained topics are never stale after a broker restart.
- While disconnected, events are dropped rather than queued (`seq` shows the
  gap); retained topics catch up on reconnect.
- `homeassistant = true` publishes one device-discovery message
  (`homeassistant/device/used_<id>/config`): presence (enum), occupied
  (occupancy binary sensor), idle seconds, CPU, GPU, workload CPU, workload
  count.
- Publish only; `used` subscribes to nothing. TLS via `ssl://` / `wss://`.

### 6.5 Desktop: notifications and tray

Both are for the person at the machine; neither changes what is reported.

- **Notifications** (`[notify] enabled = true`) are an output like webhooks,
  sent by the daemon: a workload started/finished here, "welcome back" when
  someone returns to a machine with work running, and override changes.
  Backends: macOS `osascript`, Linux `org.freedesktop.Notifications`,
  Windows WinRT toast via PowerShell. Formatting is shared (`notify.Message`).
- **Tray** (`used tray`) is a separate process and an ordinary API client:
  it follows `/v1/events` and posts `/v1/override`. A dot shows the state
  (green active, amber present, grey unused, ring = workloads running); the
  menu offers hold 1h / hold / release / automatic. `internal/tray` holds a
  portable menu model plus one backend per OS: NSStatusItem via purego/objc
  (macOS), Shell_NotifyIcon (Windows), StatusNotifierItem + dbusmenu
  (Linux; GNOME needs the AppIndicator extension).

### 6.6 Local API (same socket/pipe as §3)

```
GET  /v1/status              current state + metrics
GET  /v1/events              NDJSON stream of events
GET  /v1/explain             signals, rule matches, timers — why we're in this state
POST /v1/workloads …         see §4.2
POST /v1/override            { "mode": "hold" | "release", "for": "2h" }
GET  /metrics                Prometheus exposition (optional, can bind TCP)
```

Access: socket mode `0660`, group `used`; on Windows, pipe ACL for
Administrators + the interactive user. Workload registration additionally
requires a token (`/etc/used/token`, admin-readable) so ordinary user
processes cannot register leases — unless `workloads.allow_user_leases`.

### 6.7 CLI

```
used status [--json]        one-shot state
used watch  [--json]        live stream
used explain                human-readable decision trace
used exec --tag T -- cmd    run as ignored workload
used hold [--for D] / used release [--for D]
used install / uninstall    register daemon + session agent
used config check           validate config, list rule warnings
used version
```

---

## 7. Configuration

`/etc/used/config.toml` (Linux/macOS), `%ProgramData%\used\config.toml`
(Windows). Hot-reloaded; invalid config is rejected and the old one kept.

```toml
socket = ""                        # default: $XDG_RUNTIME_DIR/used.sock or per-user temp dir

[presence]
preset             = "balanced"    # strict | balanced | lenient; keys below override it
idle_after         = "2m"
unused_after       = "10m"
present_max        = "45m"
evidence_hold      = "30s"
lock_is_unused     = true
count_remote_input = false
linux_raw_input    = false         # read /dev/input (needs `input` group)
unattributed       = 0.0           # weight multiplier for evidence without a PID; 0 ignores it
signals = { fullscreen = 1.0, camera_mic = 1.0, audio = 0.7, assertion = 0.6 }  # 0 disables

[[ignore]]
cgroup = "/kubepods.slice/**"

[[ignore]]
path = "/opt/farm/bin/*"
tag  = "farm"

[workloads]
max_ttl = "24h"

[report]
heartbeat = "60s"
app_names = false                  # include app-provided detail strings in evidence
labels    = { site = "office-a" }

[[webhook]]
url     = "https://scheduler.internal/hooks/used"
secret  = "env:USED_HOOK_SECRET"   # or file:/path, or a literal
events  = ["state.changed", "heartbeat", "override.changed"]   # empty = all
headers = { Authorization = "env:USED_HOOK_TOKEN" }

[mqtt]
broker        = "tcp://mqtt.lan:1883"  # ssl://, ws://, wss:// too
username      = "used"
password      = "env:USED_MQTT_PASSWORD"
topic         = ""                     # default used/<hostname>
qos           = 1
events        = []                     # event/<type> filter, empty = all
homeassistant = true                   # MQTT discovery

[notify]
enabled = false                        # desktop notifications
events  = []                           # empty = all that have a message
```

Unknown keys are rejected so typos don't silently fall back to defaults.

---

## 8. Security and privacy

- No keystroke/mouse content, no window titles, URLs, screenshots or
  clipboard. Only timestamps, states, PIDs and (opt-in) executable names.
- Nothing is sent anywhere until a webhook or MQTT broker is configured.
- No permission-prompting APIs by default (no Accessibility, Input
  Monitoring or Screen Recording on macOS).
- Ignore registrations cannot mask hardware input (§4.4).
- Secrets referenced via `env:` / `file:`, never logged.
- Daemon runs privileged only where needed; the agent runs as the session
  user with no network access of its own (talks only to the daemon).
- Deployment on others' machines: `used status` and a documented payload
  make it transparent what is reported; operators should inform users.

---

## 9. Distribution

- **Language**: Go. Pure Go wherever possible (`golang.org/x/sys`, `purego`
  for IOKit/CoreAudio/NVML, `godbus` for D-Bus). Static binaries for
  `darwin/{arm64,amd64}`, `linux/{amd64,arm64}`, `windows/{amd64,arm64}`.
- **Install**: `curl -fsSL https://…/install.sh | sh` /
  `irm https://…/install.ps1 | iex` → downloads, verifies checksum + signature,
  runs `used install`. Also Homebrew tap, Scoop/winget, `.deb`/`.rpm`.
- **`used install`** registers:
  - macOS: LaunchDaemon (`daemon`) + LaunchAgent (`agent`, per GUI session).
  - Linux: systemd system unit (`daemon`) + systemd user unit enabled
    globally (`agent`).
  - Windows: service (`daemon`) + agent spawned into each interactive session
    by the service via `WTSQueryUserToken` / `CreateProcessAsUser`
    (no per-user setup needed).
- **Release**: goreleaser; macOS codesign + notarization; Windows
  Authenticode; cosign-signed checksums; SBOM.
- **Upgrade**: replace binary + `used install` is idempotent; daemon restarts,
  queue and leases persist.

---

## 10. Repository layout

```
cmd/used/                 main, CLI wiring
internal/presence/        state machine, timers, confidence, presets
internal/probe/           Probe interface + {darwin,windows,linux} impls
  input/ lock/ fullscreen/ audio/ media/ assertions/ remote/
internal/metrics/         collectors, GPU backends, per-PID attribution
internal/workload/        registry, exec wrapper, process-tree / cgroup / job object tracking
internal/rules/           ignore + allow matchers, signature verification
internal/ipc/             socket / named pipe, peer credentials
internal/api/             local HTTP API, NDJSON, Prometheus
internal/webhook/         queue, signing, delivery
internal/store/           bbolt persistence
internal/config/          schema, validation, hot reload
internal/install/         launchd / systemd / Windows service registration
schema/                   JSON Schema for payload v1
docs/
```

---

## 11. Milestones

| # | Milestone                 | Done when |
|---|---------------------------|-----------|
| 1 | **Input + lock probes**   | `used status` shows hardware-input idle time, input source and lock state on macOS, Windows, Linux (GNOME, KDE, Sway, X11). Synthetic-input filter verified on each. |
| 2 | **State machine**         | Latch rule, timers, presets, overrides, `explain`; fully unit-tested with a fake clock and scripted signal timelines. |
| 3 | **Daemon/agent split**    | IPC, peer credentials, multi-session, headless mode, `install`/`uninstall`. |
| 4 | **Evidence probes**       | Fullscreen, audio, camera/mic, assertions — all with PID attribution. |
| 5 | **Workload exclusion**    | `used exec` (incl. inside containers), API leases, static rules; evidence filtering verified with a scheduled fullscreen GPU job. |
| 6 | **Metrics**               | CPU/mem/disk/net/thermal/power + GPU backends; total/user/workload split. |
| 7 | **Outputs**               | Webhooks (signing, queue, retry), NDJSON, Prometheus, payload JSON Schema. |
| 8 | **Release**               | Signed/notarized builds, install scripts, package managers. |

Milestone 1 is the riskiest (Wayland fragmentation, Windows injected-input
filtering) and should be spiked first.

---

## 12. Risks and open questions

- **Wayland**: no generic fullscreen/foreground info; idle via
  compositor-specific protocols. Mitigation: per-compositor backends,
  lower confidence, optional raw input.
- **Windows low-level hooks** can be dropped by the OS if the hook thread
  stalls (`LowLevelHooksTimeout`); needs a dedicated, never-blocking thread
  and fallback to `GetLastInputInfo`.
- **macOS process tracking** without cgroups can miss double-forked daemons
  that escape the process group; documented limitation, mitigated by
  `parent`/`path` rules.
- **Remote-desktop detection on macOS** is best effort.
- **Per-PID GPU on macOS** is approximate.
- **Open**: should `present_max` reset on *any* attributable evidence change
  (e.g. new song starts), or only on input? Current choice: only on input.
- **Open**: machine-level state when one session is `active` and another is
  `present` on a terminal server — report both (current), but is a
  per-session webhook stream needed?

---

## 13. Implementation status

Done (first cut):

- `sense`: probes for macOS (HID idle, lock/console, display assertions),
  Windows (injected-input-filtering hooks, lock via input desktop, RDP,
  fullscreen/D3D/presentation with foreground PID), Linux (raw evdev opt-in,
  Mutter, X11, logind idle/lock/remote/inhibitors).
- `presence`: latch rule, timers, evidence hold, presets, overrides, trace.
- `workload`: leases, ancestry-based ownership, glob rules (name/path/cgroup).
- `sense` evidence: audio playback + microphone with PIDs (macOS 14+
  CoreAudio, Linux pactl/PipeWire).
- `metrics`: CPU/memory/load/GPU with total/user/workload split. GPU via
  IOAccelerator (macOS, no per-PID), NVML + DRM sysfs (Linux, per-PID on
  NVIDIA), GPU Engine perf counters (Windows, per-PID for all vendors).
- `install`: per-user autostart: LaunchAgent, systemd user unit, HKCU Run
  key (no admin). `install.sh` / `install.ps1` with checksum verification;
  goreleaser config producing the matching assets.
- `daemon`: 1 s tick, local API over a unix socket (also on Windows),
  peer-PID lease registration (container-safe on Linux/macOS), NDJSON
  stream, signed webhooks with backoff, MQTT (retained state/status,
  last will, Home Assistant discovery), `daemon.started` event, desktop
  notifications, `used tray`.
- CLI: `status`, `watch`, `explain`, `daemon`, `exec`, `hold`, `release`,
  `auto`, `install`, `uninstall`, `config`.

Deviations from the sections above, for now:

- Single process (`used daemon`) running as the user; no daemon/agent split
  (§3) yet. Cross-user workload metrics depend on what the OS lets an
  unprivileged process see: fine on Linux (/proc, cgroups) and Windows,
  limited on macOS for root-owned workloads.
- Windows autostart briefly flashes a console window before `--background`
  hides it; a GUI-subsystem launcher would remove that.
- Webhook queue is in memory (subscriber buffer), not persisted. MQTT
  drops events while disconnected (retained topics recover).
- No API token; the socket is `0600`.
- Overrides are reported as `{"mode": …, "until": …}` rather than a string.

Verified on macOS only (probes, audio, GPU, daemon, webhooks, MQTT incl.
broker restart, exec, shutdown, plist validity, notifications, tray
start/update/quit). Linux and Windows code compiles and vets but has
not run yet.

Next: `used install --tray` (autostart the tray), CI that runs on Linux and Windows runners, persistent webhook queue,
Windows audio (WASAPI), camera detection, per-PID GPU on AMD/Intel Linux
(fdinfo), ext-idle-notify on wlroots/KDE, code-signing rules, daemon/agent
split.
