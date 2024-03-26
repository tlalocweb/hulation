package config

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"

	"github.com/IzumaNetworks/conftagz"
	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/hooks"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
	"gopkg.in/yaml.v2"
)

const (
	DEFAULT_PORT = 8080
	// DEFAULT_STORE_TYPE = "bolt"
	// DEFAULT_STORE_PATH = "izvisitors.db"
)

// Courtesy of 'mkcert' comments:
// Version can be set at link time to override debug.BuildInfo.Main.Version,
// which is "(devel)" when building from within the module. See
// golang.org/issue/29814 and golang.org/issue/29228.
// Link time: go build -ldflags "-X config.Version=$(git describe --tags)"
var Version string

type DBConfig struct {
	Username string `yaml:"user,omitempty" env:"DB_USERNAME" test:"~.+" default:"hula"`
	Password string `yaml:"pass,omitempty" env:"DB_PASSWORD"`
	Host     string `yaml:"host,omitempty" env:"DB_HOST" test:"~.+" default:"localhost"`
	DBName   string `yaml:"dbname,omitempty" env:"DB_NAME" test:"~.+" default:"hula"`
	Port     int    `yaml:"port,omitempty" env:"DB_PORT" test:"<65536,>0" default:"9000"`
	// how many times to retry a connection to the DB on startup
	Retries int `yaml:"retries,omitempty" test:">=0" default:"5"`
	// the amount of seconds to wait between retries
	DelayRetry int64 `yaml:"delay_retry,omitempty" test:">=0" default:"5"`
}

type CookieOpts struct {
	// there are various implications of prefixing cookies with a beginning _ underscore character
	// research for a later time
	CookiePrefix string `yaml:"cookie_prefix,omitempty" test:"~.+" default:"hula"`
	// the default is to set the cookie to expire in 1 year
	// this is also the default for Google Analytics
	ExpireDays int `yaml:"expire_days,omitempty" test:">0" default:"365"`
	// If set, will specifically set the SameSite attribute of the cookie
	// the default (blank) is to let hulation autodetect. In this case it will use Strict if the hulation is serving
	// the site itself
	SameSite string `yaml:"same_site,omitempty" test:"~^(Strict|Lax|None)?$" default:""`
	// if true, do not set the Secure flag on the cookie
	NoSecure bool `yaml:"no_secure,omitempty"`
	// if true, then hula will _not_ set the 'domain=' attribute of the cookie
	// By default, hula will set the 'domain=example.com' attribute on the cookie
	// to the domain of this server
	// Setting domain of the cookie is needed if other servers / subdomains will be using the hula cookies
	// and scripts in this domain.
	//
	// Example: if server abc.example.com is this hula install, and server xyz.example.com
	// is using the hula.js script, then the hula cookie will not be accessuble by xyz.example.com's scripts
	// unless the domain attribute is set to example.com.
	//
	// In most situations you should use the default - which allows any host in the same domain
	// as the domain of this server to access the hula cookies
	NoUseDomain bool `yaml:"no_use_domain,omitempty"`
}

type DefinedLander struct {
	// the name of the Lander
	Name        string `yaml:"name,omitempty" test:"~.+"`
	Description string `yaml:"description,omitempty"`
	UrlId       string `yaml:"url_id,omitempty" test:"~[^\\/]+"`
	Redirect    string `yaml:"redirect,omitempty" test:"~.+"`
	NoServe     bool   `yaml:"no_serve,omitempty"`
}

type StaticFolder struct {
	Root      string `yaml:"root,omitempty" test:"~.+"`
	URLPrefix string `yaml:"url_prefix,omitempty" test:"~\\/.+"`
	Compress  bool   `yaml:"compress,omitempty"`
	ByteRange bool   `yaml:"byte_range,omitempty"`
	Browse    bool   `yaml:"browse,omitempty"`
	Index     string `yaml:"index,omitempty"`
	// uses string representation of time.Duration
	CacheDuration string `yaml:"cache_duration,omitempty"` // test:"$(validtimeduration)"`
	MaxAge        uint   `yaml:"max_age,omitempty"`
}

const (
	HOST_USE_HOSTNAME       = "use:hostname"     // tells hulation to accept/use the hostname of the server as the Host header for this server (should not be used in production)
	ALIAS_USE_INTERFACE_IPS = "use:interfaceips" // tells hulation to accept the network interfaces IPs of the server as the Host header (should not be used in production)
)

type HookCode struct {
	// the Risor script to run
	Name string `yaml:"name" test:"~.+"`
	// can either be the script or a filename
	Risor     *string `yaml:"risor,omitempty"`
	RisorCode *string `yaml:"risor_code,omitempty"`
	// the template for all hook executions (precompiled)
	risorHookTempl *hooks.RisorHook
}

// func (hc *HookCode) GetRisorHook() *hooks.RisorHook {
// 	return hc.risorHook
// }

type VisitorHooks struct {
	Globals         map[string]interface{} `yaml:"globals,omitempty"`
	globalsTemplate *hooks.TemplateGlobalsForHooks
	// One or more Risor scripts to run on a new form submission
	OnNewFormSubmission              []*HookCode `yaml:"on_new_form_submission,omitempty"`
	onNewFormSubmissionHookTemplates []*hooks.RisorHook
	//	onNewFormSubmissionHookChain []*
	// One or more Risor scripts to run on a new visitor landing on a Lander
	OnLanderVisit              []*HookCode `yaml:"on_lander_visit,omitempty"`
	onLanderVisitHookTemplates []*hooks.RisorHook
	//	onNewLanderVisitHookChain []*hooks.HookChain

	OnNewVisitor              []*HookCode `yaml:"on_new_visitor,omitempty"`
	onNewVisitorHookTemplates []*hooks.RisorHook
	// onNewVisitorRisorHooks []*hooks.HookChain
}

func (vh *VisitorHooks) GetGlobalTemplate(mixthis ...map[string]any) map[string]any {
	if vh.globalsTemplate == nil {
		vh.globalsTemplate = hooks.NewTemplateGlobalsForHooks()
	}
	// add in any global things which should be available in ever script
	return vh.globalsTemplate.MixInGlobals(mixthis...)
}

func (vh *VisitorHooks) SubmitToHooksOnNewFormSubmission(globals map[string]interface{}, onOk hooks.OnCompleteHookFunc, onErr hooks.OnErrHookFunc) {
	for _, hc := range vh.onNewFormSubmissionHookTemplates {
		hooks.ExecuteVisitorHook(vh.GetGlobalTemplate(vh.Globals, globals), hc, onOk, onErr)
	}
}
func (vh *VisitorHooks) PrecompileHooksOnNewFormSubmission(globals map[string]interface{}) (err error) {
	vh.onNewFormSubmissionHookTemplates = make([]*hooks.RisorHook, 0)
	for _, hc := range vh.OnNewFormSubmission {
		h, err := hooks.CompileVisitorHook(vh.GetGlobalTemplate(vh.Globals, globals), hc.Name, *hc.RisorCode)
		if err != nil {
			return err
		}
		vh.onNewFormSubmissionHookTemplates = append(vh.onNewFormSubmissionHookTemplates, h)
	}
	return
}

func (vh *VisitorHooks) SubmitToHooksOnNewLanderVisit(globals map[string]interface{}, onOk hooks.OnCompleteHookFunc, onErr hooks.OnErrHookFunc) {
	for _, hc := range vh.onLanderVisitHookTemplates {
		hooks.ExecuteVisitorHook(vh.GetGlobalTemplate(vh.Globals, globals), hc, onOk, onErr)
	}
}
func (vh *VisitorHooks) PrecompileHooksOnNewLanderVisit(globals map[string]interface{}) (err error) {
	vh.onLanderVisitHookTemplates = make([]*hooks.RisorHook, 0)
	for _, hc := range vh.OnLanderVisit {
		h, err := hooks.CompileVisitorHook(vh.GetGlobalTemplate(vh.Globals, globals), hc.Name, *hc.RisorCode)
		if err != nil {
			return err
		}
		vh.onLanderVisitHookTemplates = append(vh.onLanderVisitHookTemplates, h)
	}
	return
}

func (vh *VisitorHooks) SubmitToHooksOnNewVisitor(globals map[string]interface{}, onOk hooks.OnCompleteHookFunc, onErr hooks.OnErrHookFunc) {
	for _, hc := range vh.onNewVisitorHookTemplates {
		hooks.ExecuteVisitorHook(vh.GetGlobalTemplate(vh.Globals, globals), hc, onOk, onErr)
	}
}

func (vh *VisitorHooks) PrecompileHooksOnNewVisitor(globals map[string]interface{}) (err error) {
	vh.onNewVisitorHookTemplates = make([]*hooks.RisorHook, 0)
	for _, hc := range vh.OnNewVisitor {
		h, err := hooks.CompileVisitorHook(vh.GetGlobalTemplate(vh.Globals, globals), hc.Name, *hc.RisorCode)
		if err != nil {
			return err
		}
		vh.onNewVisitorHookTemplates = append(vh.onNewVisitorHookTemplates, h)
	}
	return
}

func (h *HookCode) FinalizeConf(host string, field string, m map[string]string) (err error) {
	if h.Risor != nil {
		if h.RisorCode != nil {
			return fmt.Errorf("server[%s].hooks.%s: cannot use both risor and risor_code", host, field)
		}
		var newhookstr string
		newhookstr, err = SubstConfVars(*h.Risor, m)
		if err != nil {
			log.Errorf("error substituting vars in server[%s].hooks.%s.risor config var: %s", host, field, err.Error())
		} else {
			*h.Risor = newhookstr
		}
		// load from file
		if _, err = os.Stat(*h.Risor); err == nil {
			var newhookstr []byte
			newhookstr, err = os.ReadFile(*h.Risor)
			if err != nil {
				log.Errorf("error reading file for server[%s].hooks.%s.risor: %s", host, field, err.Error())
				err = fmt.Errorf("server[%s].hooks.%s.risor: %s", host, field, err.Error())
				return
			} else {
				var newstr string
				newstr, err = SubstConfVars(string(newhookstr), m)
				if err != nil {
					log.Errorf("error substituting vars in server[%s].hooks.%s.risor config var: %s", host, field, err.Error())
					err = fmt.Errorf("substituting vars in server[%s].hooks.%s.risor: %s", host, field, err.Error())
					return
				}
				h.RisorCode = new(string)
				*h.RisorCode = newstr
			}
		} else {
			log.Errorf("server[%s].hooks.%s.risor: file not found: %s", host, field, *h.Risor)
			err = fmt.Errorf("server[%s].hooks.%s.risor: file not found: %s", host, field, *h.Risor)
			return
		}
	}
	if h.RisorCode != nil {
		var newhookstr string
		newhookstr, err = SubstConfVars(*h.RisorCode, m)
		if err != nil {
			log.Errorf("error substituting vars in server[%s].hooks.%s.risor config var: %s", host, field, err.Error())
		} else {
			*h.RisorCode = newhookstr
		}
	}
	return
}

const ()

var default_CSP_fetch_directives = map[string]string{
	"default-src": "'self'",
	"script-src":  "'self'",
	"style-src":   "'self'",
	"img-src":     "'self'",
	"connect-src": "'self'",
	"font-src":    "'self'",
	"object-src":  "'self'",
	"media-src":   "'self'",
	"frame-src":   "'self'",
}

type CSP struct {
	FetchDirectives map[string]string `yaml:"fetch,omitempty"`
	// if set to true, will not add the default CSP directives
	NoDefaults          bool              `yaml:"no_defaults,omitempty"`
	Other               map[string]string `yaml:"other,omitempty"`
	computed_directives map[string]string
}

func (s *Server) GetCSPMap() map[string]string {
	csp := &s.CSP
	if csp.computed_directives == nil {
		csp.computed_directives = make(map[string]string)
		if !csp.NoDefaults {
			for k, v := range default_CSP_fetch_directives {
				csp.computed_directives[k] = v
			}
		}
		if csp.FetchDirectives != nil {
			for k, v := range csp.FetchDirectives {
				if csp.computed_directives[k] != "" {
					csp.computed_directives[k] = csp.computed_directives[k] + " " + v
				} else {
					csp.computed_directives[k] = v
				}
			}
		}
		if !csp.NoDefaults {
			if s.Domain != "" {
				hostcsp := fmt.Sprintf("%s *.%s", s.Domain, s.Domain)
				csp.computed_directives["default-src"] = csp.computed_directives["default-src"] + " " + hostcsp
				csp.computed_directives["script-src"] = csp.computed_directives["script-src"] + " " + hostcsp
				csp.computed_directives["frame-src"] = csp.computed_directives["frame-src"] + " " + hostcsp
				csp.computed_directives["connect-src"] = csp.computed_directives["connect-src"] + " " + hostcsp
			} else {
				log.Warnf("CSP headers will for host %s will not include domain-specific directives because no domain is set or computed", s.Host)
			}
		}
		if csp.Other != nil {
			for k, v := range csp.Other {
				csp.computed_directives[k] = v
			}
		}
	}
	return csp.computed_directives
}

// func (vh *VisitorHooks) GetOnNewFormSubmissionRisorHooks() []*hooks.HookChain {
// 	return vh.onNewFormSubmissionHookChain
// }

// func (vh *VisitorHooks) GetOnNewLanderVisitRisorHooks() []*hooks.HookChain {
// 	return vh.onNewLanderVisitHookChain
// }

// func (vh *VisitorHooks) GetOnNewVisitorRisorHooks() []*hooks.HookChain {
// 	return vh.onNewVisitorRisorHooks
// }

type Server struct {
	Host string `yaml:"host,omitempty" env:"SERVER_HOST" test:"~.+"`
	// optionally tell hulation to only run the server on the given network interfaces. By default hulation listens on all interfaces
	// this can be an ip address or a network interface name. If it's a network interface name, then hulation will listen on first IP it finds
	// Support for listening on multiple interfaces is not implemented yet b/c fiber v2 isn't super flexible with adding net.listerners when using TLS
	// so for now, it's just a single string. v3 should fix this
	ListenOn string `yaml:"listen_if,omitempty"`
	// the port to listen on - if not set, inherits the port used in hulation's global config
	Port int `yaml:"port,omitempty"`
	// optional - other names the Host header can be to match this server
	Aliases []string `yaml:"aliases,omitempty"`
	// ID should be a short random string - it is used as a parameter to the hulation server to identify the server
	// and do a check with the Host header. It must be set - there is no default. This is not a secret.
	ID     string `yaml:"id,omitempty" env:"SERVER_ID" test:"~.+"`
	Domain string `yaml:"domain,omitempty" env:"SERVER_DOMAIN"`
	// max age in days of a hello cookie - the hula cookie used for tracking visitors
	// this is the regular cookie, not the session cookie
	HelloCookieMaxAge int         `yaml:"hello_cookie_max_age,omitempty" test:">0" default:"30"`
	CORS              *CORSConfig `yaml:"cors,omitempty"`
	SSL               *SSLConfig  `yaml:"ssl,omitempty"`
	CSP               CSP         `yaml:"csp,omitempty"`
	// anything related to hulation functionality uses this prefix (optional)
	// so if PathPrefix is /hula, then the hula.js script /hula/scripts/hula.js
	// and APIs would be under /hula/api/...
	// UNIMPLEMENTED for now
	PathPrefix      string     `yaml:"path_prefix,omitempty" env:"SERVER_PATH_PREFIX"`
	APIPath         string     `yaml:"api_path,omitempty" env:"SERVER_API_PATH" test:"~\\/.+" default:"/api"`
	TurnstileSecret string     `yaml:"turnstile_secret,omitempty" env:"TURNSTILE_SECRET"`
	CookieOpts      CookieOpts `yaml:"cookie_opts"`
	// not common - will ignore port in Host header when validating - useful for local testing
	IgnorePortInHeader bool `yaml:"ignore_port_in_host"`
	// When dynamically creating the hula.js script - publish the port hula is running on
	// (this would only be done if not running hulation behind a transparent proxy - not common)
	PublishPort bool `yaml:"publish_port"`
	// When dynamically creating the hula.js script - publish which protocol hula is running on (externally visible)
	HttpScheme    string `yaml:"http_scheme,omitempty" env:"SERVER_HTTP_SCHEME" test:"~.+" default:"https"`
	CaptchaSecret string `yaml:"captcha_secret,omitempty"`
	// root is the root directory of the server - this is used to serve static files
	// static serving is optional
	Root          string `yaml:"root,omitempty" env:"SERVER_ROOT"`
	RootCompress  bool   `yaml:"root_compress,omitempty"`
	RootByteRange bool   `yaml:"root_byte_range,omitempty"`
	RootBrowse    bool   `yaml:"root_browse,omitempty"`
	RootIndex     string `yaml:"root_index,omitempty"`
	// uses string representation of time.Duration
	RootCacheDuration string `yaml:"root_cache_duration,omitempty"` // test:"$(validtimeduration)"`
	RootMaxAge        uint   `yaml:"root_max_age,omitempty" default:"3600"`

	NonRootStaticFolders []*StaticFolder  `yaml:"static_folders,omitempty"`
	Landers              []*DefinedLander `yaml:"landers,omitempty"`
	// computed string
	Hooks            *VisitorHooks `yaml:"hooks,omitempty"`
	externalUrl      string
	externalHostPort string
	// the string used for the server setup for fiber, etc. computed from Port and ListenOn
	listenOn string
	// this is just a flag used to mark the Hulation API server entry from the others - its just a marker to know which
	// actual server port/router to put the APIs on
	hulacore bool
}

func (s *Server) GetExternalUrl() string {
	return s.externalUrl
}

func (s *Server) GetExternalHostPort() string {
	return s.externalHostPort
}

// A Listener is a the entity that listens for incoming requests and processes them
// We don't configure Listeners - they are configured the config manager here, based on the
// Server configs. There is only one Listener per port. THat Listener will work for one or more servers.
// There is a separate Listener per protocol also (http1.1 http2 http3)
type Listener struct {
	listenOn     string // the address to listen on - such ":8080" or "127.0.0.1:8080
	servers      []*Server
	serverByHost map[string]*Server // same as above but by host string for fast lookup
	// the combine CORS config for all servers on this listener
	CORS *CORSConfig
	SSL  []*SSLConfig
	// the fiber app for this listener - if applicable
	FiberApp *fiber.App
	// this is just a flag used to mark the Hulation API server entry from the others - its just a marker to know which
	// actual server port/router to put the APIs on
	hulacore bool
}

func (l *Listener) IsHulaCore() bool {
	return l.hulacore
}

// returns listeOn which is a string such as:
// the address to listen on - such ":8080" or "127.0.0.1:8080
func (l *Listener) GetListenOn() string {
	return l.listenOn
}

func (l *Listener) GetServer(host string) *Server {
	return l.serverByHost[host]
}

func (l *Listener) GetServers() []*Server {
	return l.servers
}

type CORSConfig struct {
	// the default '*' is not recommended as it will often block requests from happening in some browsers
	// specificity is recommended
	// see: https://developer.mozilla.org/en-US/docs/Web/HTTP/CORS/Errors/CORSNotSupportingCredentials?utm_source=devtools&utm_medium=firefox-cors-errors&utm_campaign=default
	AllowOrigins     string `yaml:"allow_origins,omitempty" env:"CORS_ALLOW_ORIGINS"`
	AllowMethods     string `yaml:"allow_methods,omitempty" env:"CORS_ALLOW_METHODS" default:"GET,POST,HEAD,PUT,DELETE,PATCH,OPTIONS"`
	AllowHeaders     string `yaml:"allow_headers,omitempty" env:"CORS_ALLOW_HEADERS"` // default:"Origin,Content-Type,Accept,Content-Length,Accept-Language,Accept-Encoding,Connection,Access-Control-Allow-Origin"`
	AllowCredentials bool   `yaml:"allow_credentials,omitempty" env:"CORS_ALLOW_CREDENTIALS"`
	// if true, this external URL of this server will not be added to CORS allow origins
	NoAddInHula bool `yaml:"no_add_in_hula"`
	// will set the Access-Control-Allow-Origin header to the value of the originating request's host
	// this should only be set for debug / development reasons
	UnsafeAnyOrigin bool `yaml:"unsafe_any_origin" env:"UNSAFE_ANY_ORIGIN"`
}

type SSLConfig struct {
	// can be either the cert / key itself infline or a path to the cert / key
	Cert string `yaml:"cert,omitempty"`
	Key  string `yaml:"key,omitempty"`
	// if the above is a path, it moved here
	certPath string
	keyPath  string
	tlsCert  *tls.Certificate
}

func (cfg *SSLConfig) NoConfig() bool {
	return len(cfg.Cert) < 1 && len(cfg.Key) < 1
}

func (cfg *SSLConfig) GetTLSCert() *tls.Certificate {
	return cfg.tlsCert
}

func (cfg *SSLConfig) LoadSSLConfig() (err error) {
	cfg.Cert, err = SubstConfVars(cfg.Cert, map[string]string{"confdir": confDir})
	if err != nil {
		log.Errorf("error substituting vars in SSL.Cert config var: %s", err.Error())
	}
	cfg.Key, err = SubstConfVars(cfg.Key, map[string]string{"confdir": confDir})
	if err != nil {
		log.Errorf("error substituting vars in SSL.Key config var: %s", err.Error())
	}
	if len(cfg.Cert) > 0 {
		if _, err := os.Stat(cfg.Cert); err == nil {
			cfg.certPath = cfg.Cert
		} else {
			if len(cfg.Cert) < 200 {
				log.Warnf("SSL: Cert is either invalid data or file not present at path: %s", path.Clean(cfg.Cert))
			}
		}
	}
	if len(cfg.Key) > 0 {
		if _, err := os.Stat(cfg.Key); err == nil {
			cfg.keyPath = cfg.Key
		} else {
			if len(cfg.Key) < 200 {
				log.Warnf("SSL: Key is either invalid data or file not present at path: %s", path.Clean(cfg.Key))
			}
		}
	}
	// now read each file and place in struct
	if len(cfg.certPath) > 0 && len(cfg.keyPath) > 0 {
		cert, err := os.ReadFile(cfg.certPath)
		if err != nil {
			log.Fatalf("TLS: error reading cert file: %s", err.Error())
		}
		key, err := os.ReadFile(cfg.keyPath)
		if err != nil {
			log.Fatalf("TLS: error reading key file: %s", err.Error())
		}
		cfg.Cert = string(cert)
		cfg.Key = string(key)
	}
	var cert tls.Certificate
	cert, err = tls.X509KeyPair([]byte(cfg.Cert), []byte(cfg.Key))
	if err != nil {
		log.Fatalf("TLS: error parsing cert/key: %s", err.Error())
	} else {
		log.Infof("TLS: loaded cert/key")
		cfg.tlsCert = &cert
	}
	return
}

func ValidTimeDuration(val interface{}, fieldname string) bool {
	_, err := time.ParseDuration(val.(string))
	return err == nil
}

type Admin struct {
	Username string `yaml:"username,omitempty" env:"ADMIN_USERNAME" test:"~.+" default:"admin"`
	Hash     string `yaml:"hash" env:"HULA_ADMIN_HASH" test:"~.+"`
}

type Config struct {
	Admin *Admin `yaml:"admin,omitempty"`
	Port  int    `yaml:"port,omitempty" env:"APP_PORT" test:">1024,<65536" default:"8080"`
	// If true, then hula will publish the port it is running on in the hula.js script
	// in the CORS and CSP headers. Normally a port is not included in these
	// since externally it is using 80 or 443 for http or https respectively
	PublishPort bool `yaml:"publish_port,omitempty"`
	// If set, and if publish_port is true, then hula will use this port in the hula.js script
	// in the CORS and CSP headers.
	ExternalPublishPort int        `yaml:"external_publish_port,omitempty"`
	DBConfig            *DBConfig  `yaml:"dbconfig,omitempty"`
	Servers             []*Server  `yaml:"servers,omitempty"`
	CORS                CORSConfig `yaml:"cors,omitempty"`
	SSL                 *SSLConfig `yaml:"ssl,omitempty"`
	Proxies             []*Proxy   `yaml:"proxies,omitempty"`
	JWTKey              string     `yaml:"jwt_key,omitempty"`
	JWTExpiration       string     `yaml:"jwt_expiration,omitempty" test:"$(validtimeduration)" default:"72h"`
	// The hostname of the hulation server itself - format: host or host:port
	// This is used for APIs specifc to hula, visitor tracking, etc.
	// Hula will still serve the its visitor APIs to any host is published in the 'servers' section
	// See servers section.
	HulaHost string `yaml:"hula_host,omitempty" env:"HULA_HOST" test:"~.+" default:"localhost"`
	// Optional - other names the Host header can be to match this server
	HulaAliases []string `yaml:"hula_aliases,omitempty"`
	// Specifically configure the domain for Hula vs. it being derived automatically from hula_host.
	// Normally this is not set in the config
	HulaDomain string `yaml:"hula_domain,omitempty"`
	// By defalt Hulation will isten on 0.0.0.0:Port - the listen IP or network interface can be set here
	// Set the port using the Port config var
	ListenOn string `yaml:"listen_on,omitempty"`
	// allows customization of the hula.js script filename - this changes what HTTP GET path is used to serve the script
	// default: https://server.com/hula.js
	PublishedHelloScriptFilename    string `yaml:"hello_script_filename,omitempty" env:"PUBLISHED_HELLO_SCRIPT_FILENAME" test:"~[^\\/]+" default:"hula.js"`
	PublishedFormsScriptFilename    string `yaml:"forms_script_filename,omitempty" env:"PUBLISHED_FORMS_SCRIPT_FILENAME" test:"~[^\\/]+" default:"forms.js"`
	PublishedIFrameHelloFileName    string `yaml:"iframe_hello_filename,omitempty" env:"PUBLISHED_IFRAME_HELLO_FILENAME" test:"~[^\\/]+" default:"hula_hello.html"`
	PublishedIFrameNoScriptFilename string `yaml:"iframe_noscript_filename,omitempty" env:"PUBLISHED_IFRAME_NOSCRIPT_FILENAME" test:"~[^\\/]+" default:"hulans.html"`
	// use only for debugging - this will will prevent hula from looking
	// at the Host header when validating the request
	UnsafeNoHostCheck bool `yaml:"unsafe_no_host_check,omitempty" env:"UNSAFE_NO_HOST_CHECK"`
	// the amount of time we wait for all
	// methods of a visitor to return before we write the visitor to the DB
	BounceTimeout int64 `yaml:"bounce_timeout,omitempty" env:"BOUNCE_TIMEOUT" test:">0" default:"2000"`
	// Store *store.StoreConfig `yaml:"store"`
	// List of IP addresses to accept connections from
	// AcceptIPs []string `yaml:"accept_ips"`
	byServer   map[string]*Server
	byAllAlias map[string]*Server
	byListener map[string]*Listener
	// script folder - default fine if hulation exec is in the top folder of repo
	ScriptFolder             string `yaml:"script_folder,omitempty" env:"SCRIPT_FOLDER" test:"~.+" default:"{{huladir}}/scripts"`
	LocalHelloScriptFilename string `yaml:"local_hello_script_filename,omitempty" env:"LOCAL_HELLO_SCRIPT_FILENAME" test:"~[^\\/]+" default:"hello.js"`
	LocalFormsScriptFilename string `yaml:"local_forms_script_filename,omitempty" env:"LOCAL_FORMS_SCRIPT_FILENAME" test:"~[^\\/]+" default:"forms.js"`
	// the prefix in the url for all Landers
	LanderPath string `yaml:"lander_path,omitempty" test:"~\\/.+" default:"/land"`
	// the prefix for all URLs used by vistitors
	VisitorPrefix string `yaml:"visitor_prefix,omitempty" test:"~\\/.+" default:"/v"`
	// the string used for the server setup for fiber, etc. computed from Port and ListenOn
	listenOn string
}

type Proxy struct {
	// The taret URL to proxy to - such http://127.0.0.1:8080 for a local server
	Target string `yaml:"target,omitempty" test:"~.+"`
	// use by_domain if you want to proxy an entire host
	ByDomain string `yaml:"by_domain,omitempty"`
	ByPath   string `yaml:"by_path,omitempty"`
}

const (
	getDomain_re        = `(?m)(?:.+\.)?([^.]+\.[^.]+)`
	ValidIpAddressRegex = `^(([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])\.){3}([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])$`
	ValidHostnameRegex  = `^(([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]*[a-zA-Z0-9])\.)*([A-Za-z0-9]|[A-Za-z0-9][A-Za-z0-9\-]*[A-Za-z0-9])$`
)

var getDomainRE = regexp.MustCompile(getDomain_re)
var validIpAddressRE = regexp.MustCompile(ValidIpAddressRegex)
var validHostnameRE = regexp.MustCompile(ValidHostnameRegex)
var confDir string

func (cfg *Config) GetListener(listenOn string) *Listener {
	if cfg == nil {
		return nil
	}
	return cfg.byListener[listenOn]
}

func (cfg *Config) GetListeners() map[string]*Listener {
	if cfg == nil {
		return nil
	}
	return cfg.byListener
}

func (cfg *Config) GetServer(host string) *Server {
	if cfg == nil {
		return nil
	}
	return cfg.byServer[host]
}
func (cfg *Config) GetServerByAnyAlias(host string) *Server {
	if cfg == nil {
		return nil
	}
	return cfg.byAllAlias[host]
}

func LoadConfig(filename string) (*Config, error) {
	var cfg Config

	cfg.byListener = make(map[string]*Listener)

	buf, err := os.ReadFile(filename)
	if err != nil {
		return &cfg, fmt.Errorf("read file: %s", err.Error())
	}

	path, _ := filepath.Abs(filename)
	confDir = filepath.Dir(path)

	err = yaml.UnmarshalStrict(buf, &cfg)
	if err != nil {
		return nil, fmt.Errorf("yaml parse: %s,", err.Error())
	}

	log.Debugf("config: db %+v", *cfg.DBConfig)

	conftagz.RegisterTestFunc("validtimeduration", ValidTimeDuration)

	err = conftagz.Process(nil, &cfg)
	if err != nil {
		return nil, fmt.Errorf("bad config: %s,", err.Error())
	}

	if len(cfg.Servers) < 1 {
		return nil, fmt.Errorf("no servers defined")
	}

	if len(cfg.JWTKey) < 1 {
		log.Warnf("JWTKey not set - this is not recommended. Tokens will be invalid on server restart.")
		cfg.JWTKey, err = utils.GenerateBase64RandomString(32)
		if err != nil {
			return nil, fmt.Errorf("error generating JWTKey: %s", err.Error())
		}
	}

	cfg.ScriptFolder, err = SubstConfVars(cfg.ScriptFolder, map[string]string{"confdir": confDir})
	if err != nil {
		log.Errorf("error substituting vars in script_folder config var: %s", err.Error())
	}

	// add in the hula API server to the 'listenOn" server list first
	if len(cfg.ListenOn) < 1 {
		cfg.listenOn = fmt.Sprintf(":%d", cfg.Port)
	} else {
		res := net.ParseIP(cfg.ListenOn)
		if res == nil { // not a valid IP
			addr, err := utils.GetInterfaceIpv4Addr(cfg.ListenOn)
			if err != nil {
				log.Errorf("error getting IP for network interface %s: %s", cfg.ListenOn, err.Error())
				return nil, fmt.Errorf("error getting IP for network interface '%s': %s", cfg.ListenOn, err.Error())
			} else {
				cfg.listenOn = fmt.Sprintf("%s:%d", addr, cfg.Port)
			}
		} else {
			cfg.listenOn = fmt.Sprintf("%s:%d", res, cfg.Port)
		}
		cfg.listenOn = fmt.Sprintf("%s:%d", cfg.ListenOn, cfg.Port)
	}
	hula_server := &Server{
		hulacore: true,
		Host:     cfg.HulaHost,
	}
	cfg.byListener[cfg.listenOn] = &Listener{
		listenOn:     cfg.listenOn,
		servers:      []*Server{hula_server},
		serverByHost: make(map[string]*Server),
		hulacore:     true,
	}
	cfg.byListener[cfg.listenOn].CORS = &cfg.CORS
	if cfg.SSL != nil && !cfg.SSL.NoConfig() {
		cfg.byListener[cfg.listenOn].SSL = append(cfg.byListener[cfg.listenOn].SSL, cfg.SSL)
		hula_server.HttpScheme = "https"
	} else {
		hula_server.HttpScheme = "http"
	}
	cfg.byListener[cfg.listenOn].serverByHost["hula"] = cfg.byListener[cfg.listenOn].servers[0]

	cfg.byServer = make(map[string]*Server)
	cfg.byAllAlias = make(map[string]*Server)
	cfg.byAllAlias[cfg.HulaHost] = hula_server
	cfg.byServer[cfg.HulaHost] = hula_server

	for _, s := range cfg.HulaAliases {
		cfg.byAllAlias[s] = hula_server
	}

	if len(cfg.HulaDomain) < 1 {
		res := getDomainRE.FindAllStringSubmatch(cfg.HulaHost, -1)
		if len(res) > 0 && len(res[0]) > 1 {
			hula_server.Domain = res[0][1]
		} else {
			hula_server.Domain = hula_server.Host
		}
	} else {
		hula_server.Domain = cfg.HulaDomain
	}
	log.Debugf("server[%s].domain (hula) = %s", hula_server.Host, hula_server.Domain)
	var hula_portstring string
	if cfg.PublishPort {
		if cfg.Port != 443 && cfg.Port != 80 {
			hula_portstring = fmt.Sprintf(":%d", cfg.Port)
		}
		if cfg.ExternalPublishPort > 0 {
			hula_portstring = fmt.Sprintf(":%d", cfg.ExternalPublishPort)
		}
	}
	hula_server.externalUrl = fmt.Sprintf("%s://%s%s", hula_server.HttpScheme, hula_server.Host, hula_portstring)
	hula_server.externalHostPort = fmt.Sprintf("%s:%d", hula_server.Host, hula_server.Port)

	for _, s := range cfg.Servers {
		if s.Port < 1 {
			s.Port = cfg.Port
		}
		if len(s.ListenOn) > 0 {
			res := net.ParseIP(s.ListenOn)
			if res == nil { // not a valid IP
				addr, err := utils.GetInterfaceIpv4Addr(s.ListenOn)
				if err != nil {
					log.Errorf("error getting IP for network interface %s: %s", s.ListenOn, err.Error())
					return nil, fmt.Errorf("error getting IP for network interface '%s': %s", s.ListenOn, err.Error())
				} else {
					s.listenOn = fmt.Sprintf("%s:%d", addr, s.Port)
				}
			} else {
				s.listenOn = fmt.Sprintf("%s:%d", res, s.Port)
			}
		}
		if len(s.listenOn) < 1 {
			s.listenOn = fmt.Sprintf(":%d", s.Port)
		}
		listenerforserver, ok := cfg.byListener[s.listenOn]
		if ok {
			listenerforserver.servers = append(listenerforserver.servers, s)
		} else {
			listenerforserver = &Listener{listenOn: s.listenOn, servers: []*Server{s}, serverByHost: make(map[string]*Server)}
			cfg.byListener[s.listenOn] = listenerforserver
		}
		if listenerforserver.CORS != nil {
			log.Warnf("CORS config for listener %s will be overwritten - muliple CORS config for same listeber FIXME", s.listenOn)
		}
		listenerforserver.CORS = &cfg.CORS
		if s.SSL != nil && !s.SSL.NoConfig() {
			err = s.SSL.LoadSSLConfig()
			if err != nil {
				return nil, fmt.Errorf("bad ssl config for server %s: %s", s.Host, err.Error())
			}
			listenerforserver.SSL = append(listenerforserver.SSL, s.SSL)
		} else if len(listenerforserver.SSL) > 0 {
			log.Warnf("SSL config for server %s is missing but TLS will be used by other servers on same port.", s.Host)
		}
		// if host has a special directive, work it out first:
		// TODO

		if len(s.Domain) < 1 {
			res := getDomainRE.FindAllStringSubmatch(s.Host, -1)
			if len(res) > 0 && len(res[0]) > 1 {
				s.Domain = res[0][1]
			} else {
				s.Domain = s.Host
			}
			log.Debugf("server[%s].domain = %s", s.Host, s.Domain)
		}
		var portstring string
		if s.PublishPort {
			if cfg.Port != 443 && cfg.Port != 80 {
				portstring = fmt.Sprintf(":%d", cfg.Port)
			}
		}
		s.externalUrl = fmt.Sprintf("%s://%s%s", s.HttpScheme, s.Host, portstring)
		s.externalHostPort = fmt.Sprintf("%s:%d", s.Host, cfg.Port)
		cfg.byServer[s.Host] = s
		cfg.byAllAlias[s.Host] = s
		for _, a := range s.Aliases {
			_, ok := cfg.byServer[a]
			if ok {
				log.Errorf(`alias "%s" for server config %s already referenced`, a, s.Host)
				return nil, fmt.Errorf(`alias "%s" for server config %s already referenced`, a, s.Host)
			}
			if validIpAddressRE.MatchString(a) || validHostnameRE.MatchString(a) {
				log.Debugf(`server[%s] alias %s`, s.Host, a)
				cfg.byServer[a] = s
			} else {
				log.Errorf(`bad alias "%s" for server config: %s`, a, s.Host)
				return nil, fmt.Errorf(`bad alias "%s" for server config: %s`, a, s.Host)
			}
		}
		// log.Debugf("server[%s].externalUrl = %s", s.Host, s.externalUrl)
		// log.Debugf("cfg.byListener[%s].servers = %+v", s.listenOn, listenerforserver.servers)
		// log.Debugf("cfg.byListener[%s].serverByHost[%s] = %+v", s.listenOn, s.Host, listenerforserver.serverByHost)
		cfg.byListener[s.listenOn].serverByHost[s.Host] = s
		// look at aliases
		for _, a := range s.Aliases {
			_, ok := cfg.byServer[a]
			if ok {
				log.Errorf(`alias "%s" for server config %s already referenced`, a, s.Host)
			}
			if validIpAddressRE.MatchString(a) || validHostnameRE.MatchString(a) {
				log.Debugf(`server[%s] alias %s`, s.Host, a)
				cfg.byServer[a] = s
			} else {
				log.Errorf(`bad alias "%s" for server config: %s`, a, s.Host)
			}
		}
		s.Root, err = SubstConfVars(s.Root, map[string]string{"confdir": confDir})
		if err != nil {
			log.Errorf("error substituting vars in server[%s].root config var: %s", s.Host, err.Error())
		}
		for _, f := range s.NonRootStaticFolders {
			f.Root, err = SubstConfVars(f.Root, map[string]string{"confdir": confDir})
			if err != nil {
				log.Errorf("error substituting vars in server[%s].static_folders[%s].root config var: %s", s.Host, f.URLPrefix, err.Error())
			}
		}
		if s.Hooks == nil {
			s.Hooks = &VisitorHooks{}
		}
		if len(s.Hooks.OnNewFormSubmission) > 0 {
			for _, h := range s.Hooks.OnNewFormSubmission {
				err = h.FinalizeConf(s.Host, "on_new_form_submission", map[string]string{"confdir": confDir, "staticdir": s.Root})
				if err != nil {
					return nil, err
				}
			}
		}
		if len(s.Hooks.OnLanderVisit) > 0 {
			for _, h := range s.Hooks.OnLanderVisit {
				err = h.FinalizeConf(s.Host, "on_new_lander_visit", map[string]string{"confdir": confDir, "staticdir": s.Root})
				if err != nil {
					return nil, err
				}
			}
		}
		if len(s.Hooks.OnNewVisitor) > 0 {
			for _, h := range s.Hooks.OnNewVisitor {
				err = h.FinalizeConf(s.Host, "on_new_visitor", map[string]string{"confdir": confDir, "staticdir": s.Root})
				if err != nil {
					return nil, err
				}
			}
		}
	}

	if !cfg.CORS.NoAddInHula {
		alloworigins := cfg.CORS.AllowOrigins
		for _, s := range cfg.Servers {
			if len(alloworigins) > 0 {
				alloworigins += ", "
			}
			alloworigins += s.externalUrl
		}
		alloworigins += ", " + hula_server.externalUrl
		cfg.CORS.AllowOrigins = alloworigins
		log.Debugf("CORS.AllowOrigins = %s", alloworigins)
	}

	if cfg.SSL != nil {
		// skip if these are both entirely empty - it means the user
		// did not want SSL, otherwise let the error handling work
		if !cfg.SSL.NoConfig() {
			err = cfg.SSL.LoadSSLConfig()
			if err != nil {
				return nil, fmt.Errorf("bad ssl config: %s", err.Error())
			}
		}
	}

	return &cfg, nil
}

func GetDSNFromConfig(cfg *Config) string {
	return fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s",
		cfg.DBConfig.Username, cfg.DBConfig.Password, cfg.DBConfig.Host, cfg.DBConfig.Port, cfg.DBConfig.DBName)
}
