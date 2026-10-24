// _adam.js - reads an ADAM-4017 analog input module.
//
// Files starting with "_" are not loaded. Rename to adam.js to use it, after
// enabling the "adam" input in violink.json.
//
// The "adam" input polls the module with "#01\r" every second (see "poll"
// in the config) and the module answers ">+04.123+10.000+00.512...\r".
// The input's framing (start ">", end "\r") cuts that into
// "+04.123+10.000+00.512...", and adam.values() turns it into numbers.

const script = {
    inputs: ["adam"],
    output: "main",
};

// Scaling per channel: a 4-20 mA sensor read as 0-10 V over a 500 ohm
// resistor gives 2-10 V. Change to match the real sensors.
const channels = [
    { name: "level", min: 2, max: 10, from: 0, to: 5 },     // 0-5 m
    { name: "temp",  min: 2, max: 10, from: -20, to: 60 },  // -20..60 °C
];

function scale(v, c) {
    return c.from + (v - c.min) * (c.to - c.from) / (c.max - c.min);
}

function onInput(input, value) {
    const volts = adam.values(value);
    if (volts.length < channels.length) {
        warn("short reply from ADAM:", value);
        return;
    }
    const parts = channels.map((c, i) => {
        const v = scale(volts[i], c);
        record(c.name, v);
        return v.toFixed(2);
    });
    return "ADAM," + parts.join(",");
}
