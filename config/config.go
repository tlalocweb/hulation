package config

import (
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/IzumaNetworks/conftagz"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
	"gopkg.in/yaml.v2"
)

const (
	DEFAULT_PORT = 8080
	// DEFAULT_STORE_TYPE = "bolt"
	// DEFAULT_STORE_PATH = "izvisitors.db"
)

type DBConfig struct {
	Username string `yaml:"user,omitempty" env:"DB_USERNAME" test:"~.+" default:"hula"`
	Password string `yaml:"pass,omitempty" env:"DB_PASSWORD"`
	Host     string `yaml:"host,omitempty" env:"DB_HOST" test:"~.+" default:"localhost"`
	DBName   string `yaml:"dbname,omitempty" env:"DB_NAME" test:"~.+" default:"hula"`
	Port     int    `yaml:"port,omitempty" env:"DB_PORT" test:"<65536,>0" default:"9000"`
}

type CookieOpts struct {
	// there are various implications of prefixing cookies with a beginning _ underscore character
	// research for a later time
	CookiePrefix string `yaml:"cookie_prefix,omitempty" env:"SERVER_COOKIE_PREFIX" test:"~.+" default:"hula"`
	// the default is to set the cookie to expire in 1 year
	// this is also the default for Google Analytics
	ExpireDays int `yaml:"expire_days,omitempty" env:"COOKIE_EXPIRE_DAYS" test:">0" default:"365"`
	// If set, will specifically set the SameSite attribute of the cookie
	// the default (blank) is to let hulation autodetect. In this case it will use Strict if the hulation is serving
	// the site itself
	SameSite string `yaml:"same_site,omitempty" env:"COOKIE_SAME_SITE" test:"~^(Strict|Lax|None)?$" default:""`
	// if true, do not set the Secure flag on the cookie
	NoSecure bool `yaml:"no_secure,omitempty" env:"COOKIE_NO_SECURE"`
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

type Server struct {
	Host string `yaml:"host,omitempty" env:"SERVER_HOST" test:"~.+"`
	// optional - other names the Host header can be to match this server
	Aliases []string `yaml:"aliases,omitempty"`
	// ID should be a short random string - it is used as a parameter to the hulation server to identify the server
	// and do a check with the Host header. It must be set - there is no default. This is not a secret.
	ID     string `yaml:"id,omitempty" env:"SERVER_ID" test:"~.+"`
	Domain string `yaml:"domain,omitempty" env:"SERVER_DOMAIN"`
	// max age in days of a hello cookie - the hula cookie used for tracking visitors
	// this is the regular cookie, not the session cookie
	HelloCookieMaxAge int `yaml:"hello_cookie_max_age,omitempty" test:">0" default:"30"`
	// anything related to hulation functionality uses this prefix (optional)
	// so if PathPrefix is /hula, then the hula.js script /hula/scripts/hula.js
	// and APIs would be under /hula/api/...
	// UNIMPLEMENTED for now
	PathPrefix      string      `yaml:"path_prefix,omitempty" env:"SERVER_PATH_PREFIX"`
	APIPath         string      `yaml:"api_path,omitempty" env:"SERVER_API_PATH" test:"~\\/.+" default:"/api"`
	TurnstileSecret string      `yaml:"turnstile_secret,omitempty" env:"TURNSTILE_SECRET"`
	CookieOpts      *CookieOpts `yaml:"cookie_opts,omitempty"`
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
	RootMaxAge        uint   `yaml:"root_max_age,omitempty"`

	NonRootStaticFolders []*StaticFolder `yaml:"static_folders,omitempty"`
	// computed string
	externalUrl      string
	externalHostPort string
}

func (s *Server) GetExternalUrl() string {
	return s.externalUrl
}

func (s *Server) GetExternalHostPort() string {
	return s.externalHostPort
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
	Cert string `yaml:"cert,omitempty" env:"SSL_CERT"`
	Key  string `yaml:"key,omitempty" env:"SSL_KEY"`
	// if the above is a path, it moved here
	certPath string
	keyPath  string
	tlsCert  *tls.Certificate
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
		}
	}
	if len(cfg.Key) > 0 {
		if _, err := os.Stat(cfg.Key); err == nil {
			cfg.keyPath = cfg.Key
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
	Admin         *Admin      `yaml:"admin,omitempty"`
	Port          int         `yaml:"port,omitempty" env:"APP_PORT" test:">1024,<65536" default:"8080"`
	DBConfig      *DBConfig   `yaml:"dbconfig,omitempty"`
	Servers       []*Server   `yaml:"servers,omitempty"`
	CORS          *CORSConfig `yaml:"cors,omitempty"`
	SSL           *SSLConfig  `yaml:"ssl,omitempty"`
	Proxies       []*Proxy    `yaml:"proxies,omitempty"`
	JWTKey        string      `yaml:"jwt_key,omitempty"`
	JWTExpiration string      `yaml:"jwt_expiration,omitempty" test:"$(validtimeduration)" default:"72h"`
	// the hostname of the hulation server itself - format: host or host:port
	// this is used for APIs specifc to hula, visitor tracking, etc.
	// Hulation will still serve the its visitor APIs to any host which uses a Host header that matches its host ID
	// See servers section.
	HulaHost string `yaml:"hula_host,omitempty" env:"HULA_HOST" test:"~.+" default:"localhost"`
	// allows customization of the hula.js script filename - this changes what HTTP GET path is used to serve the script
	// default: https://server.com/hula.js
	PublishedHelloScriptFilename    string `yaml:"hello_script_filename,omitempty" env:"PUBLISHED_HELLO_SCRIPT_FILENAME" test:"~[^\\/]+" default:"hula.js"`
	PublishedFormsScriptFilename    string `yaml:"forms_script_filename,omitempty" env:"PUBLISHED_FORMS_SCRIPT_FILENAME" test:"~[^\\/]+" default:"forms.js"`
	PublishedIFrameHelloFileName    string `yaml:"iframe_hello_filename,omitempty" env:"PUBLISHED_IFRAME_HELLO_FILENAME" test:"~[^\\/]+" default:"hula_hello.html"`
	PublishedIFrameNoScriptFilename string `yaml:"iframe_noscript_filename,omitempty" env:"PUBLISHED_IFRAME_NOSCRIPT_FILENAME" test:"~[^\\/]+" default:"hulans.html"`
	// use only for debugging - this will will prevent hila from looking
	// at the Host header when validating the request
	UnsafeNoHostCheck bool `yaml:"unsafe_no_host_check,omitempty" env:"UNSAFE_NO_HOST_CHECK"`
	// the amount of time we wait for all
	// methods of a visitor to return before we write the visitor to the DB
	BounceTimeout int64 `yaml:"bounce_timeout,omitempty" env:"BOUNCE_TIMEOUT" test:">0" default:"2000"`
	// Store *store.StoreConfig `yaml:"store"`
	// List of IP addresses to accept connections from
	// AcceptIPs []string `yaml:"accept_ips"`
	byServer map[string]*Server
	// script folder - default fine if hulation exec is in the top folder of repo
	ScriptFolder             string `yaml:"script_folder,omitempty" env:"SCRIPT_FOLDER" test:"~.+" default:"{{huladir}}/scripts"`
	LocalHelloScriptFilename string `yaml:"local_hello_script_filename,omitempty" env:"LOCAL_HELLO_SCRIPT_FILENAME" test:"~[^\\/]+" default:"hello.js"`
	LocalFormsScriptFilename string `yaml:"local_forms_script_filename,omitempty" env:"LOCAL_FORMS_SCRIPT_FILENAME" test:"~[^\\/]+" default:"forms.js"`
	// the prefix in the url for all Landers
	LanderPath string `yaml:"lander_path,omitempty" test:"~\\/.+" default:"/land"`
	// the prefix for all URLs used by vistitors
	VisitorPrefix string `yaml:"visitor_prefix,omitempty" test:"~\\/.+" default:"/v"`
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

func (cfg *Config) GetServer(host string) *Server {
	if cfg == nil {
		return nil
	}
	return cfg.byServer[host]
}

func LoadConfig(filename string) (*Config, error) {
	var cfg Config

	buf, err := os.ReadFile(filename)
	if err != nil {
		return &cfg, fmt.Errorf("read file: %s", err.Error())
	}

	path, _ := filepath.Abs(filename)
	confDir := filepath.Dir(path)

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

	cfg.byServer = make(map[string]*Server)
	for _, s := range cfg.Servers {
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

	}

	if cfg.CORS != nil {
		if !cfg.CORS.NoAddInHula {
			alloworigins := cfg.CORS.AllowOrigins
			for _, s := range cfg.Servers {
				if len(alloworigins) > 0 {
					alloworigins += ", "
				}
				alloworigins += s.externalUrl
			}
			cfg.CORS.AllowOrigins = alloworigins
			log.Debugf("CORS.AllowOrigins = %s", alloworigins)
		}
	}

	if cfg.SSL != nil {
		// skip if these are both entirely empty - it means the user
		// did not want SSL, otherwise let the error handling work
		if len(cfg.SSL.Cert) > 0 || len(cfg.SSL.Key) > 1 {
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
