const script = {
    inputs: ["inputName"],   // inputs to listen to
    output: "outputName",     // where returned values go
    interval: 60000,    // ms between onTimer() calls (leave out for none)
};

function onStart() { }                 // once, after loading


function onInput(input, value) { }    // Every time a input gets a valid message.

function onTimer() { }

function onStop() { }                  // once, at shutdown