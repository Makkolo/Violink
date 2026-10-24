// baro.js - turns a barometer's plain text into an NMEA XDR sentence.
//
//   in:   P= 1013.25 hPa
//   out:  $WIXDR,P,1.01325,B,BARO*hh      (pressure in bar)
//
// Also keeps a 3-hour history, and logs the pressure tendency once an hour.

const script = {
    inputs: ["baro"],
    output: "main",
    interval: 3600000, // once an hour
};

function onInput(input, value) {
    const m = value.match(/P=\s*([\d.]+)\s*hPa/);
    if (!m) {
        return; // not a pressure line
    }
    const hPa = parseFloat(m[1]);
    if (hPa < 850 || hPa > 1100) {
        warn("pressure out of range, ignored:", hPa);
        return;
    }
    record("hPa", hPa);
    return nmea.wrap(`WIXDR,P,${(hPa / 1000).toFixed(5)},B,BARO`);
}

function onTimer() {
    const now = average("hPa", 5);
    if (now === null) {
        return; // no data in the last 5 minutes
    }
    const mean = average("hPa", 180);
    const range = highest("hPa", 180) - lowest("hPa", 180);
    log(`pressure ${now.toFixed(1)} hPa, 3 h mean ${mean.toFixed(1)} hPa, ` +
        `3 h range ${range.toFixed(1)} hPa, ${samples("hPa", 180)} samples`);
    // Nothing returned: this only goes to the log.
}
