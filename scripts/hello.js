(function() {
    var topwindow = window;
    var rootwindow = window;
    if (window.location != window.parent.location) {
        console.log("hello.js: in iframe")
        rootwindow = window.parent;
    }
    console.log('Hello from hello.js: {{b}},  {{apipath}}');

    const randid = function(){
        return Math.random().toString(20).substring(2, 12)
    }

    var vc = null

    var GetAPIUrl = function(apipath) {
        let postfix = "?h={{hostid}}&b={{b}}&r=" + randid();
        if (window.location.origin == "{{hulaurl}}") {
            // if the script is being served from the same host name as the API being called, then
            // we dont need to include the 'o' param, b/c the 'Host' header will be enough
            return window.location.origin + "{{visitorprefix}}" + apipath + postfix;
        } else {
            var visithost = "{{visithost}}";
            // BREAKS. because rootwindow may be from a different origin host name - so it will not work as its a cross-origin frame
            //            return window.location.origin + "{{visitorprefix}}" + apipath + postfix + "&o=" + encodeURIComponent(rootwindow.location.host);
            if (visithost.length > 0) {
                return window.location.origin + "{{visitorprefix}}" + apipath + postfix + "&o=" + encodeURIComponent(visithost);
            } else {
                return window.location.origin + "{{visitorprefix}}" + apipath + postfix;
            }
        }

        // let m;
        // const regexorigin = /(https?\:\/\/[^/]+).*/gm;
        // m = regexorigin.exec(rootwindow.location);
        // if (m !== null) {
        //     if (m.length > 1) {
        //         console.log('Base url: ${m[1]}')
        //         return m[1] + "/v";
        //     }
        //     // // The result can be accessed through the `m`-variable.
        //     // m.forEach((match, groupIndex) => {
        //     //     console.log(`Found match, group ${groupIndex}: ${match}`);
        //     // });
        // }
        // console.log("using default base url");
        // return "{{hulaurl}}/v"
    }

    var HulaSubmitForm = function(formid, url, captcha, data, onsuccess, onerror) {

        if (typeof(data) != "object") {
            console.log("HulaSubmitForm: 'data' is not an object");
            if (onerror) {
                onerror(null, "data is not an object")
            }
            return;
        }
        var xhr = new XMLHttpRequest();

        // NOTE: bounce map - will not work as bounce already used - why not put the sscookie in the payload also?

        xhr.open("POST", GetAPIUrl('/sub/'+formid), true);
        xhr.setRequestHeader("Content-Type", "application/json;charset=UTF-8");
        xhr.send(JSON.stringify(
            { 
                url: url,
                fields: data,
                vc: vc,
                captcha: captcha
            }
        ));

        xhr.onload = function() {
            console.log("hello.js: in onload() xhr")
            if (xhr.readyState == 4 && xhr.status == 200) {
                console.log("xhr response: " + xhr.responseText);
                if (onsuccess) {
                    onsuccess(xhr.responseText)
                }
            } else {
                console.log("xhr error: " + xhr.responseText + ", status: " + xhr.status);
                if (onerror) {
                    onerror(xhr.responseText)
                }
            }
        }
    }

    if (!topwindow.Hula) {
        topwindow.Hula = {}
    }
    topwindow.Hula.SubmitForm = HulaSubmitForm
//    topwindow.Hula.FindForm = HulaFindForm
    topwindow.Hula.FormData = {}

    // --- Phase 4c.1 consent surface ---
    //
    // Visitor's consent state for this page. Three sources merge into
    // _hulaConsent (highest precedence first):
    //   1. window.Hula.setConsent({analytics, marketing}) — called by
    //      the host page's CMP after the user picks a preference.
    //   2. navigator.globalPrivacyControl — W3C GPC. true = binding
    //      marketing opt-out (analytics still proceeds under
    //      legitimate-interest unless the server is in opt_in mode).
    //   3. server-default applied at /v/hello when no consent payload
    //      arrives (set via per-server consent_mode).
    //
    // The state shipped to /v/hello is whatever the page told us +
    // the GPC reading at request time. The server runs the final
    // resolution; this script only reports.
    var _hulaConsent = null;
    if (typeof navigator !== "undefined" && navigator.globalPrivacyControl === true) {
        _hulaConsent = { analytics: true, marketing: false, source: "gpc" };
    }
    topwindow.Hula.setConsent = function (s) {
        if (typeof s !== "object" || s === null) return;
        _hulaConsent = {
            analytics: !!s.analytics,
            marketing: !!s.marketing,
            source: "cmp"
        };
        // Optional: re-fire /v/hello on consent change so the server
        // can land a delayed event. Out of scope for stage 4c.1.
    };
    topwindow.Hula.getConsent = function () { return _hulaConsent; };

    // var hula_prefix = "{{cookieprefix}}";
    // var hulahost = "{{hulahost}}";

    // var hellocookiename = hula_prefix + "_hello";

    // implement the function getCookie
    // var getCookie = function(name) {
    //     var value = "; " + document.cookie;
    //     var parts = value.split("; " + name + "=");

    //     if (parts.length == 2) {
    //         return parts.pop().split(";").shift();
    //     }
    // }

    // fina a cookie named "cookieprefix" and print it
    // var hellocookie = getCookie(hellocookiename);
    // console.log("hello_cookie: " + hellocookie);
    
    // if (!hellocookie || hellocookie.length == 0) {
    //     console.log("hello_cookie is empty");
    // }

    var url = (window.location != window.parent.location)
    ? document.referrer
    : document.location.href;
    console.log("url: " + url);
    if (url != "{{thisurl}}") {
        console.error("url mismatch: " + url + " != {{thisurl}} - is document.referrer not reliable?");
    }
    url = encodeURIComponent(url);
    // make an async API call to the server using POST and send a JSON message
    var xhr = new XMLHttpRequest();
    // send a json messafe to the server in the POST body 

    xhr.open("POST", GetAPIUrl("/hello"), true);
//    xhr.withCredentials = true;
    xhr.setRequestHeader("Content-Type", "application/json;charset=UTF-8");
    var helloPayload = {
        hello: "world",
        b: "{{b}}",
        e: 1,
        url: url,
    };
    if (_hulaConsent) {
        helloPayload.consent = {
            analytics: _hulaConsent.analytics,
            marketing: _hulaConsent.marketing,
        };
    }
    xhr.send(JSON.stringify(helloPayload));
    xhr.onload = function() {
        console.log("hello.js: in onload() xhr")
        if (xhr.readyState == 4 && xhr.status == 200) {
            try {
                var resp = JSON.parse(xhr.responseText)
                vc = resp.vc
                console.log("hello.js: vc: " + vc);
            } catch(e) {
                console.error("hello.js: error parsing JSON: " + e)
                return
            }
            console.log("xhr response: " + xhr.responseText);

        } else {
            console.error("hulla hello xhr error: " + xhr.responseText + ", status: " + xhr.status);
        }
    }


    topwindow.addEventListener("load", (event) => {
        console.log("hello.js: page is fully loaded");
        topwindow.addEventListener("message", (event) => {
            console.log("hello.js: in message event:")
            console.dir(event)
            try {
                var dat = JSON.parse(event.data)
            } catch(e) {
                console.error("hello.js: error parsing JSON: " + e)
                return
            }
            HulaSubmitForm(dat.formid, dat.url, dat.captcha, dat.fields, function(resp) {
                // on success
                console.log("hello.js: HulaSubmitForm success: " + resp)
                var msg = {
                    ok: true,
                    resp: resp,
                    vc: vc,
                    // data: dat,
                    unique: dat.unique
                }
                var msg_s = JSON.stringify(msg)
                rootwindow.postMessage(msg_s, event.origin)
            }, function(resp, err) {
                // on error
                console.log("hello.js: HulaSubmitForm error: " + resp)                
                var msg = {
                    ok: false,
                    resp: resp,
                    err: err,
                    // data: dat,
                    unique: dat.unique
                }
                var msg_s = JSON.stringify(msg)
                rootwindow.postMessage(msg_s, event.origin)

            });

        }, false);
    
        //        HulaFindForm()
      });


    
})()
