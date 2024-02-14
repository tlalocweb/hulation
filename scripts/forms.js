(function() {
    var topwindow = window;
    // if (window.location != window.parent.location) {


    var HulaFindForm = function(data) {

        var currentForm = null;

        var processHulaElement = function(attr) {
            if (attr.name.startsWith('data-hula-submit')) {
                console.log("hula submit button found: " + attr.name);
                window.Hula.FormData[currentForm].submitbtn = attr.ownerElement
//                console.dir(attr)
                window.Hula.FormData[currentForm].submitbtn.addEventListener('click', function(e) {
                    e.preventDefault();
                    console.log("Hula: submit button clicked")
                    HulaSubmitForm(currentForm, window.Hula.FormData[currentForm].fields, 
                        attr.ownerElement.getAttribute('data-hula-onsuccess'), 
                        attr.ownerElement.getAttribute('data-hula-onfailure'))
                })
                return true
            }
            if (attr.name.startsWith('data-hula-turnstile')) {
                console.log("hula turnstile found: " + attr.name);
                window.Hula.FormData[currentForm].turnstile = attr.ownerElement
//                console.dir(attr)
                return true
            }
            if (attr.name.startsWith('data-hula')) {
                console.log("hula attribute found: " + attr.name);
  //              console.dir(attr)
                console.log("hula attribute value: " + attr.ownerElement.value)
                window.Hula.FormData[currentForm].fields[attr.value] = attr.ownerElement.value
                window.Hula.FormData[currentForm].fieldsel[attr.value] = attr.ownerElement
                return true
            }

            return false
        }
    

        const forms = document.querySelectorAll('form');

        // Function to check if an element has a data attribute
        function hasHulaAttribute(element) {
          const attributes = Array.from(element.attributes);
          return attributes.some(processHulaElement);
        }
        
        // Loop through each form
        forms.forEach((form, formIndex) => {
          console.log(`Form ${formIndex + 1}:`);
//          console.dir(form)
  
            if (form.hasAttribute('data-hula')) {
                currentForm = form.getAttribute('data-hula')
                console.log("currentForm: " + currentForm)
                topwindow.Hula.FormData[currentForm] = {}
                topwindow.Hula.FormData[currentForm]['fields'] = {}
                topwindow.Hula.FormData[currentForm]['fieldsel'] = {}
                // Find all the input fields inside the current form

                const inputFields = form.querySelectorAll('input, textarea, select, button');
                
                // Loop through each input field
                inputFields.forEach((field, fieldIndex) => {
                    if (hasHulaAttribute(field)) {
                        //   // Get the name of the data attribute and its value
                        //   const dataAttributeName = field.getAttribute('data-name');
                        //   const dataAttributeValue = field.getAttribute('data-value');
                        
                        // Print the data attribute name and value
                        console.log(`  Field ${fieldIndex + 1}:`);
                        console.dir(field)
                        //   console.log(`    Data Attribute Name: ${dataAttributeName}`);
                        //   console.log(`    Data Attribute Value: ${dataAttributeValue}`);
                    }
                });
            }
        });
    }

    var randid = function(){
        return Math.random().toString(20).substring(2, 12)
    }

    const findOriginRE = /^https?\:\/\/[^:]+(?:\:[0-9]+)?/gmi;
    var iframeorigin = ""
    // template engine could fill in or not
    const hulaorigin = "{{hulaorigin}}"

    if (hulaorigin == "{{hulaorigin}}" || hulaorigin == "") {
        // if we did not get the script from the hula server via template, we need to find the origin
        // by looking at the src attribute of the hula iframe

        var hulaframe = document.getElementById("hulaframe")
        if (hulaframe) {
            iframeorigin = hulaframe.getAttribute("src")
            m = findOriginRE.exec(iframeorigin)
            if (m != null) {
                iframeorigin = m[0]
            }
        }
    } else {
        iframeorigin = hulaorigin
    }

    // another optional template variable
    const hulaframeid_def = "{{hulaframeid}}"
    var hulaframeid = ""
    if (hulaframeid_def == "{{hulaframeid}}" || hulaframeid_def == "") {
        hulaframeid = "hulaframe"
    } else {
        hulaframeid = hulaframeid_def
    }

    var HulaSubmitForm = function(formid, data, onsuccess, onerror) {
            
            console.log("HulaSubmitForm: formid: " + formid)

            if (typeof(data) != "object") {
                console.error("HulaSubmitForm: 'data' is not an object");
                return;
            }

            data = { formid: formid, data: data, unique: randid() }

            var iframeWin = document.getElementById("hulaframe").contentWindow        

            var dat = JSON.stringify( data )
            console.log("HulaSubmitForm: sending to iframe: " + iframeorigin + ">> " + dat)
            
            // setup listener for response
            window.addEventListener("message", function(event) {
                if (event.origin != iframeorigin) {
//                    console.error("HulaSubmitForm: received message from unexpected origin: " + event.origin)
                    return
                }
                console.log("HulaSubmitForm: received message from iframe: " + event.origin + ">> " + event.data)
                console.dir(event)
                if (event.data == "success") {
                    if (onsuccess) {
                        onsuccess(event.data)
                    }
                } else {
                    if (onerror) {
                        onerror(event.data)
                    }
                }
            }, false);
            
            iframeWin.postMessage(dat,iframeorigin);

        }

    if (!topwindow.Hula) {
        topwindow.Hula = {}
    }
    topwindow.Hula.SubmitForm = HulaSubmitForm
    topwindow.Hula.FindForm = HulaFindForm
    topwindow.Hula.FormData = {}

    topwindow.addEventListener("load", (event) => {
        console.log("hello.js: page is fully loaded");
        HulaFindForm()
    });
})();