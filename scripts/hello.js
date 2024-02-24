(function() {
    var topwindow = window;
    var rootwindow = window;
    if (window.location != window.parent.location) {
        rootwindow = window.parent;
    }
    console.log('Hello from hello.js: {{b}},  {{apipath}}');

    const randid = function(){
        return Math.random().toString(20).substring(2, 12)
    }

    var vc = null


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

        xhr.open("POST", "{{hulaurl}}/v/sub/"+formid+"?h={{hostid}}&r="+randid(), true);
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
    url = encodeURIComponent(url);
    console.log("url: " + url);

    // make an async API call to the server using POST and send a JSON message
    var xhr = new XMLHttpRequest();
    // send a json messafe to the server in the POST body 

    xhr.open("POST", "{{hulaurl}}/v/hello?b={{b}}&h={{hostid}}", true);
//    xhr.withCredentials = true;
    xhr.setRequestHeader("Content-Type", "application/json;charset=UTF-8");
    xhr.send(JSON.stringify(
        { 
            hello: "world", 
            b: "{{b}}",
            e: 1,
            url: url,
        }
    ));
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
