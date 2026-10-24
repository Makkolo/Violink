// wind.js - passes wind sentences on, and sends a 10-minute summary.
//
// Input "wind" delivers NMEA MWV sentences like
//   $WIMWV,214,R,7.4,M,A*2B
// (direction, reference, speed, unit, status). Broken sentences are dropped,
// good ones are passed on unchanged, and once a minute a summary is sent:
//   $PVLWND,<10-min mean>,<10-min gust>,<mean direction>*hh

const script = {
    inputs: ["wind"],   // listen to this input
    output: "main",     // returned values go here
    interval: 60000,    // onTimer() once a minute
};

function onStart() {
    // The last direction from before a restart, so the summary has a value right away.
    log("starting, last direction before restart:", load("direction", "unknown"));
}

function onInput(input, value) {
    if (!nmea.valid(value)) {
        warn("bad checksum, dropped:", value);
        return; // nothing returned = nothing sent
    }
    const f = nmea.fields(value);    // ["WIMWV", "214", "R", "7.4", "M", "A"]
    if (f[0].slice(2) !== "MWV" || f[5] !== "A") {
        return; // not an MWV sentence, or the sensor flags it as invalid
    }
    const direction = parseFloat(f[1]);
    const speed = parseFloat(f[3]);

    record("speed", speed);           // history for average()/highest()
    record("dirX", Math.cos(direction * Math.PI / 180));
    record("dirY", Math.sin(direction * Math.PI / 180));
    save("direction", direction);     // survives a restart

    return value; // pass the sentence on unchanged
}

function onTimer() {
    const mean = average("speed", 10);
    if (mean === null) {
        return; // no data in the last 10 minutes
    }
    const gust = highest("speed", 10);

    // Directions are averaged as vectors, otherwise 350° and 10° would average to 180°.
    const x = average("dirX", 10);
    const y = average("dirY", 10);
    let dir = Math.atan2(y, x) * 180 / Math.PI;
    if (dir < 0) dir += 360;

    return nmea.wrap(`PVLWND,${mean.toFixed(1)},${gust.toFixed(1)},${dir.toFixed(0)}`);
}
