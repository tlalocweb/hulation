(function() {
    console.log('Hello from hello.js: {{b}},  {{apipath}}');

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
            console.log("xhr response: " + xhr.responseText);
        } else {
            console.log("xhr error: " + xhr.responseText + ", status: " + xhr.status);
        }
    }
    
})()
