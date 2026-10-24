// combined.js - one line built from several inputs, every 5 seconds.
//
// Shows how a script can use inputs it does not listen to: last("name")
// gives the newest value of any input, age("name") how old it is. Old data
// is sent as empty fields instead of pretending it is current.
//
//   out:  WX,<wind speed>,<pressure hPa>

const script = {
    inputs: [],          // no events: this script only runs on its timer
    output: "main",
    interval: 5000,
};

const maxAgeSec = 10;    // older than this counts as missing

function onTimer() {
    let speed = "";
    const wind = last("wind");
    if (wind !== null && age("wind") < maxAgeSec && nmea.valid(wind)) {
        speed = nmea.fields(wind)[3];
    }

    let pressure = "";
    const baro = last("baro");
    if (baro !== null && age("baro") < maxAgeSec) {
        const m = baro.match(/P=\s*([\d.]+)/);
        if (m) pressure = m[1];
    }

    if (speed === "" && pressure === "") {
        return; // nothing to report
    }
    return `WX,${speed},${pressure}`;
}
