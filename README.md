<p align="center">
  <img src="assets/github/violink-banner-1280.png" alt="Violink — scriptable I/O bridge · COM · TCP · UDP → scripts → COM · TCP · UDP" width="100%">
</p>

<p align="center">
  A <b>Virtual</b> <b>I/O</b> <b>Link</b>
</p>

<p align="center">
  <a href="../../releases/latest">Download</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#writing-scripts">Scripts</a> ·
  <a href="#simulation">Simulation</a> ·
  <a href="#building">Building</a>
</p>

---

## What it does

Allows flexible data and protocol conversion by running self made javascript files between the chosen inputs and output. Scripts are loaded from the scripts folder on startup, no recompiling is needed. Multiple inputs can be used in one script, and any input can be used in multiple scripts.

```
  inputs                    scripts                      outputs
  ──────                    ───────                      ───────
  COM   (RS-232 / RS-485)   wind.js   ─ parse, check,     TCP server (to all clients)
  TCP   client or server ─▶ baro.js     convert, average ─▶ TCP client
  UDP                       combined.js                   UDP
                            ...                           COM
```

- **Inputs**: COM, TCP client, TCP server, UDP. Polling for equipment that only answer when asked.
- **Framing**: CR LF by default; any start and/or end marker (`$`…`\r\n`, STX…ETX, `>`…`\r`).
  Broken and overlong values are dropped.
- **Scripts**: plain JavaScript (ES5 + most of ES6). One script can listen to several inputs,
  read the last value of any input, and run on a timer.
- **Aggregates**: `average`, `highest`, `lowest`, `significant` (H1/3) over the last *X* minutes.
- **Persistence**: `save()`/`load()` and the aggregate history survive a restart.
- **Web UI**: status, input setup, commands to sensors, simulation, output monitor, log.
- **Simulation**: virtual inputs that send patterns on a cycle, to test scripts without hardware.
- **Runs hidden** in the system tray (Windows, KDE, GNOME). Log files rotate by size.
- **Linux and Windows**, x64. No installer, no runtime to install.

## Quick start

1. Download the zip (Windows) or tar.gz (Linux) from [Releases](../../releases/latest) and unpack it anywhere.
2. Start `violink.exe` / `./violink`. The violet icon appears in the system tray.
3. Right-click the icon → **Open web UI** (or browse to <http://127.0.0.1:8470>).
4. On the **Simulation** page, start `wind` and `baro`, then watch the **Monitor** page:
   the example scripts turn the simulated sensors into the lines sent on TCP port 4100.
5. Connect your end program, or test with `nc 127.0.0.1 4100` / PuTTY (raw, port 4100).

Then edit `violink.json` and the scripts as desired.

### Program folder

Everything lives next to the executable by default:

```
violink.exe            the program (Linux: violink)
violink.json           inputs, outputs and settings
scripts/               *.js, loaded at startup (names starting with _ are skipped)
simulations/           *.json, virtual inputs for testing
data/persist.json      saved values and history (created automatically)
logs/violink.log       log files (created automatically, rotated by size)
```

Command line options, mostly for testing:

| Option | |
|---|---|
| `-console` | no tray icon, log to the console |
| `-dir <folder>` | use another program folder |
| `-config <file>` | use another config file |
| `-version` | print the version |

## Configuration

`violink.json` is plain JSON. Fields that are left out get their default value.
The full example is in [`runtime/violink.json`](runtime/violink.json).

```jsonc
{
  "tray": true,
  "webui":   { "enabled": true, "listen": "127.0.0.1:8470" },   // ":8470" = whole network
  "log":     { "folder": "logs", "level": "info", "maxSizeKB": 1024, "maxFiles": 5 },
  "persist": { "file": "data/persist.json", "saveIntervalSec": 60, "maxSeriesMinutes": 180 },
  "scripts": { "folder": "scripts", "timeoutMs": 1000, "queueLength": 64 },
  "simulations": { "folder": "simulations" },
  "inputs":  [ ... ],
  "outputs": [ ... ]
}
```

Log files stays below about `maxSizeKB × (maxFiles + 1)`.

### Inputs

| Field | Used by | |
|---|---|---|
| `name` | all | used in scripts, must be unique |
| `type` | all | `tcp-client`, `tcp-server`, `udp`, `com` |
| `enabled` | all | `false` keeps it in the file but does not open it |
| `address` | tcp, udp | client: `host:port` · server/udp: listen address like `:4001` |
| `remote` | udp | where commands go (empty = whoever sent the last datagram) |
| `port`, `baud`, `dataBits`, `parity`, `stopBits` | com | `COM3` / `/dev/ttyUSB0`, `9600`, `8`, `none`, `"1"` |
| `framing` | all | see below |
| `poll` | all | `[{ "command": "#01\r", "intervalMs": 1000 }]` |
| `idleTimeoutSec` | all | reconnect when nothing arrives for this long (0 = never) |
| `note` | all | free text |

**Framing** decides where one value ends and the next begins:

| Field | Default | |
|---|---|---|
| `start` | `""` | optional start marker; bytes before it are skipped |
| `end` | `"\r\n"` | end marker |
| `maxLength` | `4096` | longer values are dropped as corrupt |
| `keepMarkers` | `false` | keep start/end in the value (handy for NMEA: keeps `$`…`*hh`) |
| `idleResetMs` | `0` | drop a half-received value after this much silence |

Examples: NMEA `{"start": "$", "end": "\r\n"}` · STX/ETX `{"start": "\u0002", "end": "\u0003"}` ·
ADAM-4000 `{"start": ">", "end": "\r"}`.
With a start marker, a new start before the end means the previous value was cut short; it is dropped.

### Outputs

| Field | | |
|---|---|---|
| `type` | | `tcp-server` (sends to all connected clients), `tcp-client`, `udp`, `com` |
| `address` | | server: listen address · client/udp: `host:port` |
| `lineEnd` | `"\r\n"` | added after every value |
| `resendLastOnConnect` | `false` | a newly connected TCP client gets the last value at once |

Outputs keep no queue: only the newest value matters, and if an output is not
connected the value is dropped (and shown as "not sent" in the web UI).

## Writing scripts

A script is a `.js` file in `scripts/`. It describes itself in a `script`
object and implements the functions it needs. Whatever `onInput` or `onTimer`
returns goes to the script's output.

```js
const script = {
    inputs: ["wind"],   // inputs to listen to
    output: "main",     // where returned values go
    interval: 60000,    // ms between onTimer() calls (leave out for none)
};

function onStart() { }                 // once, after loading

function onInput(input, value) {       // every value from one of the inputs
    if (!nmea.valid(value)) return;    // return nothing = send nothing
    const f = nmea.fields(value);      // ["WIMWV", "214", "R", "7.4", "M", "A"]
    record("speed", parseFloat(f[3])); // keep history for average() etc.
    return value;                      // pass it on
}

function onTimer() {
    const mean = average("speed", 10); // last 10 minutes, null if no data
    if (mean === null) return;
    return nmea.wrap(`PVLWND,${mean.toFixed(1)},${highest("speed", 10).toFixed(1)}`);
}

function onStop() { }                  // once, at shutdown
```

Return a string or number to send one value, an array to send several, nothing (or `false`) to send nothing.

### Functions available in scripts

| Function | |
|---|---|
| `log(...)`, `warn(...)`, `error(...)`, `console.log(...)` | write to the log, prefixed with the script name |
| `send(output, value)` | send to any output (besides the return value) |
| `last(input)` | last value of any input, or `null` |
| `lastTime(input)`, `age(input)` | when it arrived (ms since 1970) / how many seconds ago |
| `command(input, text)` | send a command to the device behind an input |
| `save(key, value)`, `load(key, fallback)`, `forget(key)` | values that survive a restart |
| `record(key, number)` | add a number to a history series |
| `average(key, min)`, `highest(key, min)`, `lowest(key, min)` | over the last *min* minutes, `null` without data |
| `significant(key, min)` | mean of the highest third (H1/3), e.g. Hs from individual wave heights |
| `samples(key, min)` | number of values in the window |
| `nmea.checksum(s)`, `nmea.wrap(body)`, `nmea.valid(s)`, `nmea.fields(s)` | NMEA 0183 helpers |
| `adam.read(addr, ch)`, `adam.values(reply)` | ADAM-4000 command string / reply to numbers |

Keys for `save` and `record` are per script, so two scripts can use the same key names.
History older than `persist.maxSeriesMinutes` is dropped.

A script that throws an error, or runs longer than `scripts.timeoutMs`, is
stopped for that call and logged; it keeps running for the next value. Each
script has its own JavaScript engine and runs in parallel with the others.

The [example scripts](runtime/scripts) show a pass-through with a 10-minute
summary (`wind.js`), a format conversion (`baro.js`), a script combining
several inputs on a timer (`combined.js`) and an ADAM module (`_adam.js`).

### Advantech ADAM modules

- **ADAM-4000** (RS-485): a simple ASCII protocol, e.g. `#01\r` → `>+04.123+10.000…\r`.
  Works today as a `com` input with a `poll` command and framing `">"` … `"\r"`;
  `adam.values()` turns the reply into numbers. See `scripts/_adam.js`.
- **ADAM-6000** (Ethernet): Modbus TCP, which needs a Modbus input (planned).

## Simulation

Each `.json` file in `simulations/` is a virtual data source. While it runs,
its lines are delivered to an input exactly as if they had been received.
Start, stop, edit and preview simulations on the web UI's **Simulation** page.

```json
{
  "name": "wind",
  "input": "wind",
  "intervalMs": 1000,
  "order": "sequence",
  "checksum": "nmea",
  "autoStart": false,
  "lines": [ "$WIMWV,{walk:180:260:4:0},R,{wave:3:12:60:1},M,A" ]
}
```

| Pattern | |
|---|---|
| `{rand:min:max:dec}` | random value |
| `{walk:min:max:step:dec}` | random walk, moves at most *step* per tick (realistic drift) |
| `{wave:min:max:periodSec:dec}` | sine wave |
| `{ramp:min:max:periodSec:dec}` | sawtooth |
| `{count}`, `{count:start:step}` | counter |
| `{pick:a\|b\|c}` | one of the options |
| `{time}`, `{nmeatime}`, `{nmeadate}`, `{iso}` | local `hh:mm:ss`, UTC `hhmmss.ss`, UTC `ddmmyy`, ISO 8601 |

`order`: `sequence` (one line per tick, in turn), `all` (every line each tick) or `random`.
`checksum: "nmea"` adds `*hh` to lines starting with `$` or `!`.

> Leave `autoStart` off on production machines: a running simulation feeds its
> input alongside the real sensor.

## Running on Windows 24/7

Violink is a tray program, so it runs in a logged-on user session. Locking the
PC is fine; logging off stops it.

Run **`install-autostart.bat`** in the program folder once. It creates a
scheduled task that starts Violink 30 seconds after logon, has no time limit
(normal scheduled tasks stop after 3 days), and restarts it within a minute if
it ever crashes. `remove-autostart.bat` undoes it.

On Linux, `./install-desktop.sh --autostart` adds a menu entry and starts Violink at login.
On a machine without a desktop, run `./violink -console`.

## Web UI

| Page | |
|---|---|
| Overview | every input, script and output with status, last value and counters |
| Inputs | add, edit and delete inputs (saved to `violink.json`, only that input restarts) · send commands · live received values |
| Simulation | manage simulations · inject a single value |
| Monitor | live values sent on every output, and on any input |
| Log | recent log lines, filtered by level |

It listens on `127.0.0.1` only by default, because it can change the config and
send commands to sensors. Set `webui.listen` to `":8470"` to reach it from
other machines, on a trusted network.

## Building

Needs Go 1.26 or newer and `make` (plus `zip` for `make release`).
Everything is pure Go, so both platforms build from Linux without a C compiler.

```sh
# Debian / WSL: install Go from https://go.dev/dl (the apt package is too old)
sudo apt install make zip

git clone https://github.com/<you>/violink.git
cd violink
make deps          # once: downloads dependencies, writes go.sum (commit it)
make               # build/linux-amd64/violink and build/windows-amd64/violink.exe
make run           # build for Linux and run in the console
go test ./...      # framing, checksum and aggregate tests
```

`make` copies the example config, scripts and simulations next to each
executable, but never overwrites files that are already there, so your test
setup in `build/` survives a rebuild. VS Code: <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>B</kbd>
builds, <kbd>F5</kbd> debugs from `build/linux-amd64`.


## Project layout

```
cmd/violink/          the program (package main)
  main.go             startup and shutdown
  config.go           violink.json
  logger.go           log files with rotation
  framing.go          cutting byte streams into values
  input.go            inputs and protocol drivers (tcp, udp, com)
  output.go           outputs and protocol drivers
  processing.go       loading and running scripts
  scriptfuncs.go      Go functions available to scripts
  persist.go          saved values and history, aggregate functions
  simulation.go       virtual inputs
  webui.go            web UI server and JSON API
  tray.go             system tray icon
  web/                web UI page (built into the executable)
  icons/              tray icons (built into the executable)
  rsrc_windows_amd64.syso   exe icon and version info for Windows
runtime/              files placed next to the executable: config, scripts, simulations
packaging/            platform extras: Windows autostart, Linux desktop entry
assets/               logo, banner and icons
tools/                build helpers
.github/workflows/    build check and release
```

### Todo

- **A new input protocol** 
  - Files
  - Modbus
  - MQTT
  - OPC
- **Improvements**
  - TCP/UDP address whitelist
  - Web server hardening
  - Code review
  - Optimization
  - Error handling and recovery
  - Script memory limit
  - Limits and verification of config values

## License

[MIT](LICENSE)
