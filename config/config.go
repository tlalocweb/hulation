package config

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/tlalocweb/hulation/backend"
	"github.com/tlalocweb/hulation/hooks"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
	"go.izuma.io/conftagz"
	"golang.org/x/crypto/acme/autocert"
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
var BuildDate string

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
	// often hula may be run on a port which defers from the external port used by visitor. I.e. running on 8080 but externally on 443
	// in this case, NoticePort if true, will consider two different ports as different servers. If not set, hula will ignore the port
	// during a lander redirect or direct page serve.
	NoticePort bool `yaml:"notice_port"`
}

type DefinedForm struct {
	// the name of the Form
	Name string `yaml:"name" test:"~.+"`
	// an optional description of the form
	Description string `yaml:"description"`
	// A feedback template message. This will be sent in the reply
	// to the caller when the form is successfully submitted
	Feedback string `yaml:"feedback"`
	// uses one of the supported captcha types
	// See: TURNSTILE_CAPTCHA, GOOGLE_RECAPTCHA, GOOGLE_RECAPTCHA3
	Captcha string `json:"captcha" yaml:"captcha"`
	// the schema of the form
	// jsonschema of the fields required in this FormModel
	// see: https://github.com/santhosh-tekuri/jsonschema
	Schema string `yaml:"schema,omitempty" test:"~.+"`
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

// GitCredentials holds authentication for git operations.
type GitCredentials struct {
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// GitRefConfig specifies which git ref to check out.
// Branch: a branch name. Tag: "semver" (any valid semver tag), "any" (any tag), or a specific tag name.
type GitRefConfig struct {
	Branch string `yaml:"branch,omitempty"`
	Tag    string `yaml:"tag,omitempty"`
}

// GitAutoDeployConfig configures automatic site deployment from a git repository.
type GitAutoDeployConfig struct {
	Repo      string          `yaml:"repo"`
	Creds     *GitCredentials `yaml:"creds,omitempty"`
	Ref       GitRefConfig    `yaml:"ref"`
	HulaBuild string          `yaml:"hula_build,omitempty" default:"production"`
	// Where to store cloned repos and build artifacts.
	// Supports {{env:*}}, {{confdir}}, {{serverid}} substitution.
	DataDir string `yaml:"data_dir,omitempty" default:"/var/hula/sitedeploy/{{serverid}}/repo"`
	// Where the built site is deployed and served from.
	// Supports {{env:*}}, {{confdir}}, {{serverid}} substitution.
	DeployDir string `yaml:"deploy_dir,omitempty" default:"/var/hula/sitedeploy/{{serverid}}/site"`
	// If true, hula will NOT automatically pull and build the site on startup.
	// By default hula pulls and builds all root_git_autodeploy sites at startup.
	NoPullOnStart bool `yaml:"no_pull_on_start,omitempty"`
}

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
	PathPrefix string `yaml:"path_prefix,omitempty" env:"SERVER_PATH_PREFIX"`
	APIPath    string `yaml:"api_path,omitempty" env:"SERVER_API_PATH" test:"~\\/.+" default:"/api"`
	//	TurnstileSecret string     `yaml:"turnstile_secret,omitempty" env:"TURNSTILE_SECRET"`
	CookieOpts CookieOpts `yaml:"cookie_opts"`
	// not common - will ignore port in Host header when validating - useful for local testing
	//	IgnorePortInHeader bool `yaml:"ignore_port_in_host"`
	// When dynamically creating the hula.js script - publish the port hula is running on
	// (this would only be done if not running hulation behind a transparent proxy - not common)
	PublishPort bool `yaml:"publish_port"`
	// When dynamically creating the hula.js script - publish which protocol hula is running on (externally visible)
	HttpScheme    string `yaml:"http_scheme,omitempty" env:"SERVER_HTTP_SCHEME" test:"~.+" default:"https"`
	CaptchaSecret string `yaml:"captcha_secret,omitempty" env:"CAPTCHA_SECRET"`
	// root is the root directory of the server - this is used to serve static files
	// static serving is optional
	Root          string `yaml:"root,omitempty" env:"SERVER_ROOT"`
	RootCompress  bool   `yaml:"root_compress,omitempty"`
	RootByteRange bool   `yaml:"root_byte_range,omitempty"`
	RootBrowse    bool   `yaml:"root_browse,omitempty"`
	RootIndex     string `yaml:"root_index" default:"index.html"`
	// uses string representation of time.Duration
	RootCacheDuration string `yaml:"root_cache_duration,omitempty"` // test:"$(validtimeduration)"`
	RootMaxAge        uint   `yaml:"root_max_age,omitempty" default:"3600"`

	NonRootStaticFolders []*StaticFolder  `yaml:"static_folders,omitempty"`
	Landers              []*DefinedLander `yaml:"landers,omitempty"`
	Forms                []*DefinedForm   `yaml:"forms,omitempty"`
	// default is {{confdir}}/{{host}}/forms
	// where 'host' is the Host field of the server this config is about
	FormSchemaFolder string `yaml:"form_schema_folder,omitempty"`
	// computed string
	Hooks    *VisitorHooks `yaml:"hooks,omitempty"`
	Backends      []*backend.BackendConfig `yaml:"backends,omitempty"`
	GitAutoDeploy *GitAutoDeployConfig    `yaml:"root_git_autodeploy,omitempty"`
	externalUrl      string
	externalHostPort string
	// the string used for the server setup for fiber, etc. computed from Port and ListenOn
	listenOn string
	// this is just a flag used to mark the Hulation API server entry from the others - its just a marker to know which
	// actual server port/router to put the APIs on
	hulacore bool
	// if this is true, the utils.GetHostConfig and similar lookups for this server based on the host header, will look both at the DNS name _and_
	// the port number. By default, the port number is ignored in the lookup
	respectPortInLookup bool
}

func (s *Server) RespectPortInLookup() bool {
	return s.respectPortInLookup
}

func (s *Server) GetExternalUrl() string {
	return s.externalUrl
}

func (s *Server) GetExternalHostPort() string {
	return s.externalHostPort
}

func (cfg *Config) GetHulaServer() (s *Server) {
	return cfg.hulaServer
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
	// ACME / Let's Encrypt manager for this listener (nil if not using ACME)
	ACMEManager *autocert.Manager
	// Port for the ACME HTTP-01 challenge listener (default: 80)
	ACMEHTTPPort int
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

type ACMEConfig struct {
	Email    string   `yaml:"email,omitempty"`
	CacheDir string   `yaml:"cache_dir,omitempty" default:"certs"`
	Domains  []string `yaml:"domains,omitempty"`
	// Port for the HTTP-01 challenge listener (default: 80)
	HTTPPort int `yaml:"http_port,omitempty" default:"80"`
}

// BadActorConfig configures the bad actor detection and blocking feature.
// Present in config = enabled. Bool fields use negative names so false (zero value) = on.
type BadActorConfig struct {
	// Set true to disable the entire feature
	Disable bool `yaml:"disable"`
	// Path to a custom signatures YAML file (optional — embedded defaults always loaded)
	SignaturesFile string `yaml:"signatures_file,omitempty"`
	// Total score an IP must reach to be blocked
	BlockThreshold int `yaml:"block_threshold" default:"50"`
	// How long a blocked IP stays blocked
	TTL string `yaml:"ttl,omitempty" test:"$(validtimeduration)" default:"24h"`
	// How often the background eviction sweep runs
	EvictionInterval string `yaml:"eviction_interval,omitempty" test:"$(validtimeduration)" default:"15m"`
	// Set true to NOT block at TCP level before TLS handshake
	NoBlockPreTLS bool `yaml:"no_block_pre_tls"`
	// Set true to NOT load known bad actors from ClickHouse on startup
	NoLoadFromDB bool `yaml:"no_load_from_db"`
	// Set true to only log matches without actually blocking (audit mode)
	DryRun bool `yaml:"dry_run,omitempty"`
}

// TLSOptions allows tuning the TLS version for a virtual host or hula itself.
type TLSOptions struct {
	// Minimum TLS version: "tls10", "tls11", "tls12", "tls13" (default: "tls12")
	MinVersion string `yaml:"min_version,omitempty"`
	// Maximum TLS version: "tls10", "tls11", "tls12", "tls13" (default: no limit)
	MaxVersion string `yaml:"max_version,omitempty"`
}

func parseTLSVersion(s string) (uint16, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tls10", "tls1.0", "1.0":
		return tls.VersionTLS10, nil
	case "tls11", "tls1.1", "1.1":
		return tls.VersionTLS11, nil
	case "tls12", "tls1.2", "1.2", "":
		return tls.VersionTLS12, nil
	case "tls13", "tls1.3", "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("unknown TLS version: %q", s)
	}
}

// GetMinVersion returns the uint16 TLS version constant. Defaults to TLS 1.2.
func (t *TLSOptions) GetMinVersion() uint16 {
	if t == nil || t.MinVersion == "" {
		return tls.VersionTLS12
	}
	v, err := parseTLSVersion(t.MinVersion)
	if err != nil {
		log.Warnf("tls config: %s, using TLS 1.2", err)
		return tls.VersionTLS12
	}
	return v
}

// GetMaxVersion returns the uint16 TLS version constant. Returns 0 (no limit) if not set.
func (t *TLSOptions) GetMaxVersion() uint16 {
	if t == nil || t.MaxVersion == "" {
		return 0
	}
	v, err := parseTLSVersion(t.MaxVersion)
	if err != nil {
		log.Warnf("tls config: %s, using no max", err)
		return 0
	}
	return v
}

type SSLConfig struct {
	// can be either the cert / key itself infline or a path to the cert / key
	Cert string `yaml:"cert,omitempty"`
	Key  string `yaml:"key,omitempty"`
	// ACME / Let's Encrypt automatic certificate management
	ACME *ACMEConfig `yaml:"acme,omitempty"`
	// TLS version controls (min/max)
	TLS *TLSOptions `yaml:"tls,omitempty"`
	// if the above is a path, it moved here
	certPath string
	keyPath  string
	tlsCert  *tls.Certificate
}

func (cfg *SSLConfig) NoConfig() bool {
	return len(cfg.Cert) < 1 && len(cfg.Key) < 1 && cfg.ACME == nil
}

func (cfg *SSLConfig) IsACME() bool {
	return cfg.ACME != nil
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
	Username     string `yaml:"username,omitempty" env:"ADMIN_USERNAME" test:"~.+" default:"admin"`
	Hash         string `yaml:"hash" env:"HULA_ADMIN_HASH" test:"~.+"`
	TotpRequired bool   `yaml:"totp_required,omitempty"`
}

type Config struct {
	Admin *Admin `yaml:"admin,omitempty"`
	Port  int    `yaml:"port,omitempty" env:"APP_PORT" test:">0,<65536" default:"8080"`
	// If true, then hula will publish the port it is running on in the hula.js script
	// in the CORS and CSP headers. Normally a port is not included in these
	// since externally it is using 80 or 443 for http or https respectively
	PublishPort bool `yaml:"publish_port,omitempty"`
	// If set, and if publish_port is true, then hula will use this port in the hula.js script
	// in the CORS and CSP headers.
	ExternalPublishPort int `yaml:"external_publish_port,omitempty"`
	// You would set this to 'https' if you are running Hula behind a reserve proxy
	// where behind the proxy it is not using https, but the reverse proxy is handling https
	// in this case all external URLs Hula publishes for hula services / APIs should be https
	ExternalScheme string     `yaml:"external_http_scheme,omitempty" env:"HULA_EXTERNAL_HTTP_SCHEME"`
	DBConfig       *DBConfig  `yaml:"dbconfig,omitempty"`
	Servers        []*Server  `yaml:"servers,omitempty"`
	CORS           CORSConfig `yaml:"cors,omitempty"`
	SSL            *SSLConfig `yaml:"ssl,omitempty"`
	// TLS certificate for hula's own admin/API endpoints.
	// Covers localhost, 127.0.0.1, ::1, and hula_host automatically.
	// Supports cert/key files, ACME, or omit for auto self-signed.
	HulaSSL        *SSLConfig `yaml:"hula_ssl,omitempty"`
	Registries map[string]*backend.RegistryConfig `yaml:"registries,omitempty"`
	BadActors  *BadActorConfig                      `yaml:"bad_actors,omitempty"`
	// Comma-separated list of log tags to enable (only these tags will log)
	LogTags   string `yaml:"log_tags,omitempty"`
	// Comma-separated list of log tags to exclude from logging
	NoLogTags string `yaml:"no_log_tags,omitempty"`
	Proxies        []*Proxy   `yaml:"proxies,omitempty"`
	JWTKey         string     `yaml:"jwt_key,omitempty"`
	// Base64url-encoded 32-byte key for encrypting TOTP secrets at rest.
	// Generate with: hulactl totp-key
	TotpEncryptionKey string `yaml:"totp_encryption_key,omitempty" env:"HULA_TOTP_ENCRYPTION_KEY"`
	// Issuer name shown in authenticator apps (default: "Hulation")
	TotpIssuer string `yaml:"totp_issuer,omitempty" default:"Hulation"`
	JWTExpiration  string     `yaml:"jwt_expiration,omitempty" test:"$(validtimeduration)" default:"72h"`
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
	// Server struct for Hula server itself
	hulaServer *Server
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

// GetServerByID returns the server with the given ID, or nil if not found.
func (cfg *Config) GetServerByID(id string) *Server {
	if cfg == nil {
		return nil
	}
	for _, s := range cfg.Servers {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func LoadConfig(filename string) (*Config, error) {
	var cfg Config

	cfg.byListener = make(map[string]*Listener)

	buf, err := os.ReadFile(filename)
	if err != nil {
		return &cfg, fmt.Errorf("read file: %s", err.Error())
	}

	fpath, _ := filepath.Abs(filename)
	confDir = filepath.Dir(fpath)

	err = yaml.UnmarshalStrict(buf, &cfg)
	if err != nil {
		return nil, fmt.Errorf("yaml parse: %s,", err.Error())
	}

	if cfg.DBConfig == nil {
		log.Errorf("no db config found in config file. this is mandatory.")
		return nil, fmt.Errorf("no db config found in config file")
	}

	cfg.DBConfig.DBName = SubstConfVarsLogErrorf(cfg.DBConfig.DBName, map[string]string{"confdir": confDir}, "dbconfig.dbname")
	cfg.DBConfig.Username = SubstConfVarsLogErrorf(cfg.DBConfig.Username, map[string]string{"confdir": confDir}, "dbconfig.user")
	cfg.DBConfig.Password = SubstConfVarsLogErrorf(cfg.DBConfig.Password, map[string]string{"confdir": confDir}, "dbconfig.pass")
	cfg.DBConfig.Host = SubstConfVarsLogErrorf(cfg.DBConfig.Host, map[string]string{"confdir": confDir}, "dbconfig.host")

	log.Debugf("config: db %+v", *cfg.DBConfig)

	// Registry credential substitution
	for name, reg := range cfg.Registries {
		if reg.Server == "" {
			return nil, fmt.Errorf("registry %q missing required 'server' field", name)
		}
		reg.Server = SubstConfVarsLogErrorf(reg.Server, map[string]string{"confdir": confDir}, fmt.Sprintf("registries[%s].server", name))
		reg.Username = SubstConfVarsLogErrorf(reg.Username, map[string]string{"confdir": confDir}, fmt.Sprintf("registries[%s].username", name))
		reg.Password = SubstConfVarsLogErrorf(reg.Password, map[string]string{"confdir": confDir}, fmt.Sprintf("registries[%s].password", name))
	}

	conftagz.RegisterTestFunc("validtimeduration", ValidTimeDuration)

	err = conftagz.Process(nil, &cfg)
	if err != nil {
		return nil, fmt.Errorf("bad config: %s,", err.Error())
	}

	// conftagz may create empty pointer structs from default tags.
	// Reset HulaSSL if it was not explicitly configured.
	if cfg.HulaSSL != nil && cfg.HulaSSL.Cert == "" && cfg.HulaSSL.Key == "" && cfg.HulaSSL.ACME != nil && cfg.HulaSSL.ACME.Email == "" {
		log.Warnf("hula_ssl not configured — will use auto-generated self-signed certificate for admin/localhost connections")
		cfg.HulaSSL = nil
	}

	// conftagz may create empty GitAutoDeploy pointer structs — nil them out.
	// A GitAutoDeployConfig with no repo AND no ref is conftagz noise, not user intent.
	for _, s := range cfg.Servers {
		if s.GitAutoDeploy != nil && s.GitAutoDeploy.Repo == "" && s.GitAutoDeploy.Ref.Branch == "" && s.GitAutoDeploy.Ref.Tag == "" {
			s.GitAutoDeploy = nil
		}
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
	} else {
		cfg.JWTKey = SubstConfVarsLogErrorf(cfg.JWTKey, map[string]string{"confdir": confDir}, "jwt_key")
	}

	cfg.ScriptFolder = SubstConfVarsLogErrorf(cfg.ScriptFolder, map[string]string{"confdir": confDir}, "script_folder")

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
	cfg.HulaHost = SubstConfVarsLogErrorf(cfg.HulaHost, map[string]string{"confdir": confDir}, "hula_host")
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
		cfg.HulaDomain = SubstConfVarsLogErrorf(cfg.HulaDomain, map[string]string{"confdir": confDir}, "hula_domain")
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
	scheme := hula_server.HttpScheme
	if len(cfg.ExternalScheme) > 0 {
		scheme = cfg.ExternalScheme
	}
	hula_server.externalUrl = fmt.Sprintf("%s://%s%s", scheme, hula_server.Host, hula_portstring)
	hula_server.externalHostPort = fmt.Sprintf("%s:%d", hula_server.Host, hula_server.Port)
	cfg.hulaServer = hula_server
	for _, s := range cfg.Servers {
		if len(s.Host) < 1 {
			return nil, fmt.Errorf("server missing host")
		}
		s.Host = SubstConfVarsLogErrorf(s.Host, map[string]string{"confdir": confDir}, fmt.Sprintf("server.host[%s]", s.Host))
		s.CaptchaSecret = SubstConfVarsLogErrorf(s.CaptchaSecret, map[string]string{"confdir": confDir}, fmt.Sprintf("server.host[%s]", s.CaptchaSecret))
		// look for server directories for config files
		var formdir = filepath.Join(confDir, s.Host, "forms")
		if len(s.FormSchemaFolder) > 0 {
			formdir = s.FormSchemaFolder
			formdir, err = SubstConfVars(formdir, map[string]string{"confdir": confDir, "host": s.Host})
			if err != nil {
				log.Errorf("error substituting vars in server[%s].form_schema_folder config var: %s", s.Host, err.Error())
			}
		}
		// check if a folder exists...
		yes, err := utils.FolderExists(formdir)
		if yes {
			log.Debugf("server[%s] forms folder: %s", s.Host, formdir)
			// read forms and append them to s.Forms
			files, err := os.ReadDir(formdir)
			if err != nil {
				log.Errorf("error reading forms folder for server %s: %s", s.Host, err.Error())
			}
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				if strings.HasSuffix(f.Name(), ".yaml") {
					var form DefinedForm
					// decode yaml of file f.Name()

					// Read the file
					fullpath := path.Join(formdir, f.Name())
					data, err := os.ReadFile(fullpath)
					if err != nil {
						log.Errorf("error reading %s: %v", fullpath, err)
					}

					// Unmarshal the YAML data into the form
					err = yaml.Unmarshal(data, &form)
					if err != nil {
						log.Errorf("error (form file %s): %v", fullpath, err)
					} else {
						log.Debugf("Found form file %s - appending to forms for server %s", f.Name(), s.Host)
						s.Forms = append(s.Forms, &form)
					}
				}
			}
		} else if err != nil {
			log.Errorf("error checking for forms folder for server %s: %s", s.Host, err.Error())
		}

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
			if s.SSL.IsACME() {
				// ACME / Let's Encrypt: collect domains and build manager on the listener
				acmeCfg := s.SSL.ACME
				cacheDir := acmeCfg.CacheDir
				if len(cacheDir) < 1 {
					cacheDir = "certs"
				}
				cacheDir, _ = SubstConfVars(cacheDir, map[string]string{"confdir": confDir})
				// collect domains: explicit list, or derive from Host + Aliases
				domains := acmeCfg.Domains
				if len(domains) == 0 {
					domains = append(domains, s.Host)
					domains = append(domains, s.Aliases...)
				}
				httpPort := acmeCfg.HTTPPort
				if httpPort == 0 {
					httpPort = 80
				}
				if listenerforserver.ACMEManager == nil {
					listenerforserver.ACMEManager = &autocert.Manager{
						Prompt:     autocert.AcceptTOS,
						Email:      acmeCfg.Email,
						Cache:      autocert.DirCache(cacheDir),
						HostPolicy: autocert.HostWhitelist(domains...),
					}
					listenerforserver.ACMEHTTPPort = httpPort
					log.Infof("ACME: created autocert manager for listener %s (domains: %v, cache: %s, http_port: %d)", s.listenOn, domains, cacheDir, httpPort)
				} else {
					// merge domains into existing manager's HostPolicy
					existingDomains := listenerforserver.ACMEManager.HostPolicy
					_ = existingDomains // previous whitelist is replaced with merged set
					// rebuild whitelist with all domains
					var allDomains []string
					for _, srv := range listenerforserver.servers {
						if srv.SSL != nil && srv.SSL.IsACME() {
							if len(srv.SSL.ACME.Domains) > 0 {
								allDomains = append(allDomains, srv.SSL.ACME.Domains...)
							} else {
								allDomains = append(allDomains, srv.Host)
								allDomains = append(allDomains, srv.Aliases...)
							}
						}
					}
					allDomains = append(allDomains, domains...)
					listenerforserver.ACMEManager.HostPolicy = autocert.HostWhitelist(allDomains...)
					log.Infof("ACME: merged domains into autocert manager for listener %s (added: %v)", s.listenOn, domains)
				}
				// ACME servers still contribute to SSL presence so TLS path is taken
				listenerforserver.SSL = append(listenerforserver.SSL, s.SSL)
			} else {
				err = s.SSL.LoadSSLConfig()
				if err != nil {
					return nil, fmt.Errorf("bad ssl config for server %s: %s", s.Host, err.Error())
				}
				listenerforserver.SSL = append(listenerforserver.SSL, s.SSL)
			}
		} else if len(listenerforserver.SSL) > 0 || listenerforserver.ACMEManager != nil {
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
		for n, a := range s.Aliases {
			a = SubstConfVarsLogErrorf(a, map[string]string{"confdir": confDir}, fmt.Sprintf("server[%s].alias[%s]", s.Host, a))
			s.Aliases[n] = a
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
		// add aliases to listener's serverByHost and to byAllAlias
		for _, a := range s.Aliases {
			cfg.byListener[s.listenOn].serverByHost[a] = s
			cfg.byAllAlias[a] = s
		}
		s.Root = SubstConfVarsLogErrorf(s.Root, map[string]string{"confdir": confDir}, fmt.Sprintf("server[%s].root", s.Host))
		for _, f := range s.NonRootStaticFolders {
			f.Root = SubstConfVarsLogErrorf(f.Root, map[string]string{"confdir": confDir}, fmt.Sprintf("server[%s].static_folders[%s].root", s.Host, f.URLPrefix))
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

		// Validate and finalize backend configs
		if len(s.Backends) > 0 {
			for i, b := range s.Backends {
				if err := b.Validate(s.Host); err != nil {
					return nil, err
				}
				// Substitute config vars in environment values
				for j, env := range b.Environment {
					b.Environment[j] = SubstConfVarsLogErrorf(env,
						map[string]string{"confdir": confDir, "host": s.Host},
						fmt.Sprintf("server[%s].backends[%s].environment[%d]", s.Host, b.ContainerName, j))
				}
				// Substitute config vars in volumes
				for j, vol := range b.Volumes {
					b.Volumes[j] = SubstConfVarsLogErrorf(vol,
						map[string]string{"confdir": confDir, "host": s.Host},
						fmt.Sprintf("server[%s].backends[%s].volumes[%d]", s.Host, b.ContainerName, j))
				}
				log.Debugf("server[%s].backends[%d]: name=%s image=%s virtual_path=%s container_path=%s",
					s.Host, i, b.ContainerName, b.Image, b.VirtualPath, b.ContainerPath)
			}
		}

		// Validate and finalize git autodeploy config
		if s.GitAutoDeploy != nil {
			gad := s.GitAutoDeploy
			if gad.Repo == "" {
				return nil, fmt.Errorf("server[%s].root_git_autodeploy: 'repo' is required", s.Host)
			}
			if gad.Ref.Branch == "" && gad.Ref.Tag == "" {
				return nil, fmt.Errorf("server[%s].root_git_autodeploy: ref must have at least one of 'branch' or 'tag'", s.Host)
			}
			if s.Root != "" {
				return nil, fmt.Errorf("server[%s]: cannot have both 'root' and 'root_git_autodeploy' — use one or the other", s.Host)
			}
			gad.Repo = SubstConfVarsLogErrorf(gad.Repo, map[string]string{"confdir": confDir, "serverid": s.ID},
				fmt.Sprintf("server[%s].root_git_autodeploy.repo", s.Host))
			if gad.Creds != nil {
				gad.Creds.Username = SubstConfVarsLogErrorf(gad.Creds.Username, map[string]string{"confdir": confDir, "serverid": s.ID},
					fmt.Sprintf("server[%s].root_git_autodeploy.creds.username", s.Host))
				gad.Creds.Password = SubstConfVarsLogErrorf(gad.Creds.Password, map[string]string{"confdir": confDir, "serverid": s.ID},
					fmt.Sprintf("server[%s].root_git_autodeploy.creds.password", s.Host))
			}
			gad.DataDir = SubstConfVarsLogErrorf(gad.DataDir, map[string]string{"confdir": confDir, "serverid": s.ID},
				fmt.Sprintf("server[%s].root_git_autodeploy.data_dir", s.Host))
			gad.DeployDir = SubstConfVarsLogErrorf(gad.DeployDir, map[string]string{"confdir": confDir, "serverid": s.ID},
				fmt.Sprintf("server[%s].root_git_autodeploy.deploy_dir", s.Host))
			// Set server Root from DeployDir so static file serving works
			s.Root = gad.DeployDir
			log.Debugf("server[%s].root_git_autodeploy: repo=%s ref.branch=%s ref.tag=%s hula_build=%s data_dir=%s deploy_dir=%s",
				s.Host, gad.Repo, gad.Ref.Branch, gad.Ref.Tag, gad.HulaBuild, gad.DataDir, gad.DeployDir)
		}
	}

	// Check for duplicate container names across all servers
	containerNames := make(map[string]string) // containerName -> serverHost
	for _, s := range cfg.Servers {
		for _, b := range s.Backends {
			if existing, ok := containerNames[b.ContainerName]; ok {
				return nil, fmt.Errorf("duplicate container_name %q: used by server %s and %s", b.ContainerName, existing, s.Host)
			}
			containerNames[b.ContainerName] = s.Host
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
		if !cfg.SSL.NoConfig() && !cfg.SSL.IsACME() {
			err = cfg.SSL.LoadSSLConfig()
			if err != nil {
				return nil, fmt.Errorf("bad ssl config: %s", err.Error())
			}
		}
	}

	// Load hula's own SSL cert if configured (static cert files)
	if cfg.HulaSSL != nil && !cfg.HulaSSL.NoConfig() && !cfg.HulaSSL.IsACME() {
		err = cfg.HulaSSL.LoadSSLConfig()
		if err != nil {
			return nil, fmt.Errorf("bad hula_ssl config: %s", err.Error())
		}
	}

	// Final pass: substitute {{env:*}} and other mustache vars in all string fields.
	// This catches any config values that weren't explicitly handled above.
	SubstConfVarsForAllStrings(&cfg, map[string]string{"confdir": confDir})

	return &cfg, nil
}

func GetDSNFromConfig(cfg *Config) string {
	return fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s",
		cfg.DBConfig.Username, cfg.DBConfig.Password, cfg.DBConfig.Host, cfg.DBConfig.Port, cfg.DBConfig.DBName)
}
