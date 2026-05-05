package main

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	// "gorm.io/driver/clickhouse"

	// _ "github.com/mailru/go-clickhouse"
	chapi "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/chzyer/readline"
	"github.com/rivo/tview"
	bolt "go.etcd.io/bbolt"
	"gopkg.in/yaml.v3"

	"github.com/tlalocweb/hulation/client"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
	hulaopaque "github.com/tlalocweb/hulation/pkg/auth/opaque"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"

	"go.izuma.io/conftagz"
)

func lastLog(logs []string) string {
	if len(logs) == 0 {
		return ""
	}
	return logs[len(logs)-1]
}

func askForConfirmation() bool {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Are you sure you want to proceed? (yes/no): ")
		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("An error occurred: %v\n", err)
			os.Exit(1)
		}

		response = strings.ToLower(strings.TrimSpace(response))

		if response == "yes" || response == "y" {
			return true
		} else if response == "no" || response == "n" {
			return false
		} else {
			fmt.Println("Please type yes or no and then press enter:")
		}
	}
}

// findHulaProcess scans /proc for a running process whose executable is named "hula".
func findHulaProcess() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("cannot read /proc: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil {
			continue
		}
		if filepath.Base(exe) == "hula" {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no running hula process found")
}

func setupInitConn(hulationconf *config.Config, dbname string) (conn *sql.DB, ctx context.Context, err error) {
	// dsn := config.GetDSNFromConfig(hulationconf)
	// fmt.Printf("Connecting to %s\n", dsn)
	//	var dsn = "clickhouse://default:@127.0.0.1:9000/db?dial_timeout=200ms&max_execution_time=60"

	fmt.Printf("testing clickhouse-go library...\n")

	conn = chapi.OpenDB(&chapi.Options{
		Addr: []string{fmt.Sprintf("%s:%d", hulationconf.DBConfig.Host, hulationconf.DBConfig.Port)},
		Auth: chapi.Auth{
			Database: dbname,
			Username: hulationconf.DBConfig.Username,
			Password: hulationconf.DBConfig.Password,
		},
		Settings: chapi.Settings{
			"max_execution_time": 60,
		},
		DialTimeout: 5 * time.Second,
		Compression: &chapi.Compression{
			Method: chapi.CompressionLZ4,
		},
	})

	conn.SetMaxIdleConns(5)
	conn.SetMaxOpenConns(10)
	conn.SetConnMaxLifetime(time.Hour)
	ctx = chapi.Context(context.Background(), chapi.WithSettings(chapi.Settings{
		"max_block_size": 10,
	}), chapi.WithProgress(func(p *chapi.Progress) {
		fmt.Println("progress: ", p)
	}))
	if err = conn.PingContext(ctx); err != nil {
		if exception, ok := err.(*chapi.Exception); ok {
			//fmt.Printf("Catch exception [%d] %s \n%s\n", exception.Code, exception.Message, exception.StackTrace)
			err = fmt.Errorf("catch exception on ping db [%d] %s \n%s\n", exception.Code, exception.Message, exception.StackTrace)
		} else {
			fmt.Printf("Error: %s\n", err.Error())
			err = fmt.Errorf("error on ping db: %w", err)
		}
	} else {
		fmt.Println("Ping OK")
	}
	return
}

var DEFAULT_CONFIG_FILE = defaultConfigFilePath()

func defaultConfigFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "hulactl.yaml"
	}
	return filepath.Join(home, ".hula", "hulactl.yaml")
}

var hulactlconfigfile string

// ServerEntry holds per-server config within hulactl.yaml.
type ServerEntry struct {
	URL      string `yaml:"url"`
	Token    string `yaml:"token,omitempty"`
	Insecure bool   `yaml:"insecure,omitempty"`
}

type HulactlConfig struct {
	LogLevel           string                  `yaml:"loglevel" flag:"loglevel" usage:"sets log level to info, warn, error, fatal, panic, debug, trace, none" default:"warn" env:"HULACTL_LOGLEVEL"`
	HulationConfigPath string                  `yaml:"hulaconf" flag:"hulaconf" usage:"path to hulation config file" default:"/etc/hulation/hulation.yaml" env:"HULA_CONF"`
	DontSaveAuth       bool                    `flag:"nosaveauth" usage:"do not save the auth token to the config file"`
	DebugMode          bool                    `yaml:"debug" flag:"debug" usage:"debug mode"`
	ANSIColors         bool                    `yaml:"colors" flag:"colors" usage:"use ANSI colors"`
	GetBodyFromFile    string                  `flag:"bodyfile" usage:"get body from file"`
	GetBodyFromStdin   bool                    `flag:"bodystdin" usage:"get body from stdin"`
	GetInteractive     bool                    `flag:"inter" usage:"get body from the terminal interactively"`                      // uses readline
	HostId             string                  `yaml:"hostid" flag:"hostid" usage:"hulation host id" default:"" env:"HULA_HOST_ID"` // needed in certain requests that emulate a visitor
	Dangerous          bool                    `flag:"dangerous" usage:"allow syncing executables and security-sensitive files"`
	AutoBuild          bool                    `flag:"autobuild" usage:"automatically trigger a staging build after changes are synced (staging-mount only)"`
	// Non-interactive auth — when set, `auth` skips the readline prompts.
	// Useful for scripted/automated auth flows (e.g., end-to-end tests).
	AuthIdentity       string                  `flag:"identity" usage:"identity for non-interactive auth" env:"HULACTL_IDENTITY"`
	AuthPassword       string                  `flag:"password" usage:"password for non-interactive auth (prefer HULACTL_PASSWORD env var)" env:"HULACTL_PASSWORD"`
	// set-password — non-interactive new-password input (env preferred over flag).
	NewPassword        string                  `flag:"newpassword" usage:"new password for set-password (prefer HULACTL_NEW_PASSWORD env var)" env:"HULACTL_NEW_PASSWORD"`
	SetPasswordUser    string                  `flag:"username" usage:"username to set the password for (default: admin)" default:"admin"`
	SetPasswordProvider string                 `flag:"provider" usage:"auth provider (default: admin)" default:"admin"`
	// auth — opt out of OPAQUE; force the legacy /api/auth/login flow.
	NoOpaque           bool                    `flag:"no-opaque" usage:"disable OPAQUE on auth; use legacy plaintext flow only"`
	// forget-opaque-record — explicit Bolt file path. No autodiscovery.
	BoltPath           string                  `flag:"bolt" usage:"path to a hula Bolt file (forget-opaque-record only)"`
	// Multi-server config
	Servers map[string]*ServerEntry `yaml:"servers,omitempty"`
	// Runtime: which server to use for this invocation (not persisted)
	Host string `yaml:"-" flag:"host" usage:"hula server URL or FQDN" env:"HULACTL_HOST"`
}

// normalizeHost ensures a URL has https:// and extracts the FQDN key.
// "hula.example.com" → ("https://hula.example.com", "hula.example.com")
// "https://hula.example.com:8443" → ("https://hula.example.com:8443", "hula.example.com:8443")
func normalizeHost(input string) (fullURL, key string) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "https://") {
		fullURL = input
		key = strings.TrimPrefix(input, "https://")
	} else if strings.HasPrefix(input, "http://") {
		fullURL = input
		key = strings.TrimPrefix(input, "http://")
	} else {
		fullURL = "https://" + input
		key = input
	}
	key = strings.TrimSuffix(key, "/")
	return
}

// resolveServer picks the server to use for this invocation.
// Returns url, token, insecure, or an error if ambiguous.
func resolveServer(cfg *HulactlConfig) (url, token string, insecure bool, err error) {
	// If --host or HULACTL_HOST is set, look it up
	if cfg.Host != "" {
		_, key := normalizeHost(cfg.Host)
		if cfg.Servers != nil {
			if entry, ok := cfg.Servers[key]; ok {
				return entry.URL, entry.Token, entry.Insecure, nil
			}
		}
		// Not found — the user may be about to auth, return the URL with no token
		fullURL, _ := normalizeHost(cfg.Host)
		return fullURL, "", false, nil
	}

	// No --host: use the only server, or error if ambiguous
	if len(cfg.Servers) == 1 {
		for _, entry := range cfg.Servers {
			return entry.URL, entry.Token, entry.Insecure, nil
		}
	}
	if len(cfg.Servers) > 1 {
		err = fmt.Errorf("multiple servers configured — use --host <url> or set HULACTL_HOST to select one")
		return
	}
	err = fmt.Errorf("no servers configured — run 'hulactl auth <url>' first")
	return
}

func doAltGetBody(config *HulactlConfig) bool {
	if config.GetBodyFromFile != "" {
		// get body from file
		return true
	}
	if config.GetBodyFromStdin {
		// get body from stdin
		return true
	}
	if config.GetInteractive {
		// get body from readline
		return true
	}
	return false
}

// get's Body from file or stdin
func getAltBody(config *HulactlConfig) (body string, err error) {
	if config.GetBodyFromFile != "" {
		// get body from file
		var data []byte
		data, err = os.ReadFile(config.GetBodyFromFile)
		if err != nil {
			err = fmt.Errorf("error reading file: %w", err)
			return
		}
		body = string(data)
		return
	}
	if config.GetBodyFromStdin {
		// get body from stdin
		fi, _ := os.Stdin.Stat()
		if (fi.Mode() & os.ModeCharDevice) == 0 {
			var bodybytes []byte
			bodybytes, err = io.ReadAll(os.Stdin)
			if err != nil {
				err = fmt.Errorf("error reading from stdin: %w", err)
				return
			}
			body = string(bodybytes)
		} else {
			err = fmt.Errorf("stdin is not a character device")
			return
		}
		return
	}
	return
}

func GetConfig(opts *HulactlConfig) (err error) {
	// Load .env file (if present) and set vars in the process environment
	dotenvVars, dotenvErr := conftagz.LoadDotEnvFile(".env")
	if dotenvErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: error loading .env file: %s\n", dotenvErr.Error())
	} else {
		for k, v := range dotenvVars {
			os.Setenv(k, v)
		}
	}

	conffilepath := DEFAULT_CONFIG_FILE
	env := os.Environ()
	// env into a map
	envmap := make(map[string]string)
	for _, e := range env {
		pair := strings.Split(e, "=")
		envmap[pair[0]] = pair[1]
	}
	if _, ok := envmap["HULACTL_CONFFILE"]; ok {
		conffilepath = envmap["HULACTL_CONFFILE"]
	}

	// if _, ok := envmap["HULACTL_LOGLEVEL"]; ok {
	// 	opts.loglevel = envmap["HULACTL_LOGLEVEL"]
	// }

	// loglevel := flag.String("loglevel", "info", "sets log level to info, warn, error, fatal, panic, debug, trace, none")
	configfile := flag.String("config", "", "config file to use")
	processed, err := conftagz.ProcessFlagTags(opts, nil)
	if err != nil {
		err = fmt.Errorf("error processing flags: %w", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Processed: %v\n", processed.GetFlagsFound())
	flag.Parse()

	var usecustomconfigfilepath bool

	var data []byte
	if configfile != nil && len(*configfile) > 0 {
		usecustomconfigfilepath = true
		conffilepath = *configfile
	}
	hulactlconfigfile = conffilepath
	data, err = os.ReadFile(conffilepath)
	if err != nil {
		if os.IsNotExist(err) {
			// if the user did not specify a custom config file, then we will use the default
			if usecustomconfigfilepath {
				err = fmt.Errorf("error reading config file (%s): %w", conffilepath, err)
				return
			}
			fmt.Printf("No config file.\n")
		} else {
			err = fmt.Errorf("error reading config file (%s): %w", conffilepath, err)
			return
		}
	}

	if len(data) > 0 {
		// Unmarshal the yaml file into the config struct
		err = yaml.Unmarshal([]byte(data), opts)
		if err != nil {
			err = fmt.Errorf("error unmarshalling config file: %w", err)
			return
		}
	}

	err = conftagz.Process(&conftagz.ConfTagOpts{
		FlagTagOpts: &conftagz.FlagFieldSubstOpts{
			Tags: processed,
		},
	}, opts)
	if err != nil {
		err = fmt.Errorf("error processing config file: %w", err)
	}

	return
}

func GetHulationServerConfigOrExit(confpath string) (hulationconf *config.Config) {
	var err error
	if len(confpath) > 0 {
		hulationconf, err = config.LoadConfig(confpath)

		if err != nil {
			fmt.Printf("Error loading config: (%s) %s", confpath, err.Error())
			os.Exit(1)
		}
	} else {
		fmt.Printf("Need the hulation.yaml config file.\n")
		os.Exit(1)
	}
	return
}

func GetHulactlClient(hulactlconfig *HulactlConfig) (c *client.Client) {
	url, token, insecure, err := resolveServer(hulactlconfig)
	if err != nil {
		fmt.Printf("Error: %s\n", err)
		os.Exit(1)
	}
	c = client.NewClient(url, token)
	if insecure {
		c.SetInsecure(true)
		// gRPC path honors the insecure flag separately — hulactl's
		// ---insecure gesture should also skip server-cert verification
		// on the gRPC handshake.
		c.InsecureSkipTLSVerify = true
	}
	if hulactlconfig.DebugMode {
		c.Noisy = true
		c.NoisyErr = true
	}
	if hulactlconfig.ANSIColors {
		c.ErrOut = func(format string, a ...any) (int, error) {
			return fmt.Printf(fmt.Sprintf(utils.Red("error: ")+"%s", format), a...)
		}
		c.Output = func(format string, a ...any) (int, error) {
			return fmt.Printf(fmt.Sprintf(utils.Grey("client: ")+"%s", format), a...)
		}
	}
	// Best-effort gRPC dial. Failures don't abort — hulactl commands
	// fall back to the legacy /api/* HTTP bridge. When DialGRPC
	// succeeds, any command that calls a Grpc*-prefixed method uses
	// gRPC; otherwise it returns ErrNoGRPC and the caller falls back.
	if derr := c.DialGRPC(); derr != nil {
		if hulactlconfig.DebugMode {
			fmt.Printf("(gRPC dial failed, falling back to HTTP: %s)\n", derr.Error())
		}
	}
	return
}

func main() {

	var confpath, command string

	hulactlconfig := &HulactlConfig{}
	err := GetConfig(hulactlconfig)
	if err != nil {
		fmt.Printf("Config error: %s\n", err.Error())
		os.Exit(1)

	}

	argz := flag.Args()
	if len(argz) > 0 {
		command = argz[0]
	}

	fmt.Fprintf(os.Stderr, "Command: %s\n", command)

	switch command {
	case CMD_GENERATEHASH:
		fi, _ := os.Stdin.Stat()
		var password string
		if (fi.Mode() & os.ModeCharDevice) == 0 {
			reader := bufio.NewReader(os.Stdin)
			password, _ = reader.ReadString('\n')
		} else {
			l, err := readline.NewEx(&readline.Config{
				//				Prompt:          "\033[31m»\033[0m ",
				//				HistoryFile:     "/tmp/readline.tmp",
				//				AutoComplete:    completer,
				//				InterruptPrompt: "^C",
				//				EOFPrompt:       "exit",

				//				HistorySearchFold:   true,
				//				FuncFilterInputRune: filterInput,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
				os.Exit(1)
			}

			fmt.Printf("Generate a password hash for the config file.\n")
			var passwordb, passwordb2 []byte
			passwordb, err = l.ReadPassword("Enter secret: ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
				os.Exit(1)
			}
			passwordb2, err = l.ReadPassword("Enter secret again: ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
				os.Exit(1)
			}
			password = string(passwordb)
			password2 := string(passwordb2)
			if password != password2 {
				fmt.Printf("Passwords do not match.\n")
				os.Exit(1)
			}
		}
		hash, err := utils.Argon2GenerateFromSecretDefaults(password)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating hash: %s\n", err.Error())
			os.Exit(1)
		}
		// Verify before emitting — fail loud rather than print a
		// hash that won't validate.
		match, err := utils.Argon2CompareHashAndSecret(password, hash)
		if err != nil || !match {
			fmt.Fprintf(os.Stderr, "ERROR: hash verification failed: %v\n", err)
			os.Exit(1)
		}
		// stdout = bare hash, nothing else. Pipes into a deploy
		// script without needing cut/awk/sed.
		fmt.Println(hash)
	case CMD_AUTH:
		// Determine which server to auth against
		// Usage: hulactl auth [URL]
		var authURL, authKey string
		if len(argz) >= 2 {
			// URL provided as argument
			authURL, authKey = normalizeHost(argz[1])
		} else if hulactlconfig.Host != "" {
			// --host flag or HULACTL_HOST env
			authURL, authKey = normalizeHost(hulactlconfig.Host)
		} else if len(hulactlconfig.Servers) == 1 {
			// Single server — use it
			for _, entry := range hulactlconfig.Servers {
				authURL = entry.URL
			}
			_, authKey = normalizeHost(authURL)
		} else if len(hulactlconfig.Servers) > 1 {
			fmt.Printf("Multiple servers configured. Specify which one:\n")
			fmt.Printf("  hulactl auth <url>\n")
			for key := range hulactlconfig.Servers {
				fmt.Printf("    %s\n", key)
			}
			os.Exit(1)
		} else {
			fmt.Printf("Usage: hulactl auth <url>\n")
			fmt.Printf("Example: hulactl auth hula.example.com\n")
			os.Exit(1)
		}

		fmt.Printf("Authenticating against %s\n", authURL)

		var identity, password string
		var l *readline.Instance

		// Non-interactive path: if --identity/--password (or HULACTL_IDENTITY/
		// HULACTL_PASSWORD) are provided, skip the readline prompts entirely.
		if hulactlconfig.AuthPassword != "" {
			if hulactlconfig.AuthIdentity != "" {
				identity = hulactlconfig.AuthIdentity
			} else {
				identity = "admin"
			}
			password = hulactlconfig.AuthPassword
		} else {
			l, err = readline.NewEx(&readline.Config{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
				os.Exit(1)
			}
			l.SetPrompt("Identity (default: admin): ")
			identity, err = l.Readline()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
				os.Exit(1)
			}
			if len(identity) < 1 {
				identity = "admin"
			}
			var pass []byte
			pass, err = l.ReadPassword("Password: ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
				os.Exit(1)
			}
			password = string(pass)
		}

		// Create a client targeting the auth URL directly
		hulaclient := client.NewClient(authURL, "")
		if hulactlconfig.DebugMode {
			hulaclient.Noisy = true
			hulaclient.NoisyErr = true
		}
		// Check if this server was previously configured as insecure
		if hulactlconfig.Servers != nil {
			if entry, ok := hulactlconfig.Servers[authKey]; ok && entry.Insecure {
				hulaclient.SetInsecure(true)
			}
		}

		// OPAQUE-only authentication. Legacy plaintext path is
		// gone — to bootstrap a fresh deploy, run
		// `hulactl set-password` (or the deploy-side
		// set-admin-password.sh) first.
		oRes, oerr := hulaclient.OpaqueLogin("admin", identity, password)
		if oerr != nil {
			fmt.Printf("Error: %s\n", oerr.Error())
			os.Exit(1)
		}
		token := oRes.JWT
		authResp := client.NewResponse()
		authResp.Finish(200, "", client.AuthResponse{
			Token:        oRes.JWT,
			TotpRequired: oRes.TotpRequired,
		})
		err = nil

		// Check if TOTP is required
		if authResp != nil && authResp.Response != nil {
			ar, ok := authResp.Response.(client.AuthResponse)
			if ok && ar.TotpRequired {
				if ar.TotpNeedsSetup {
					fmt.Printf("TOTP is required but not yet set up. Use 'totp-setup' command first.\n")
					os.Exit(1)
				}
				fmt.Printf("TOTP required. Enter code from authenticator:\n")
				l.SetPrompt("TOTP Code: ")
				totpCode, err := l.Readline()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading TOTP code: %s\n", err.Error())
					os.Exit(1)
				}
				valResp, err := hulaclient.TotpValidate(ar.TotpToken, strings.TrimSpace(totpCode), false)
				if err != nil {
					fmt.Printf("TOTP validation error: %s\n", err.Error())
					os.Exit(1)
				}
				token = valResp.JWT
			}
		}

		fmt.Printf("Token: %s\n", token)
		if !hulactlconfig.DontSaveAuth {
			// Ensure config file exists
			_, err = os.Stat(hulactlconfigfile)
			if err != nil {
				if os.IsNotExist(err) {
					if dir := filepath.Dir(hulactlconfigfile); dir != "." {
						os.MkdirAll(dir, 0700)
					}
					_, err = os.Create(hulactlconfigfile)
					if err != nil {
						fmt.Printf("Error creating config file: %s\n", err.Error())
						os.Exit(1)
					}
				} else {
					fmt.Printf("Error checking for config file: %s\n", err.Error())
					os.Exit(1)
				}
			}
			// Save under servers.<key>
			err = utils.ModifyYamlFile(hulactlconfigfile, []string{"servers", authKey, "url"}, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Value: authURL,
			})
			if err != nil {
				fmt.Printf("Error saving URL to config file: %s\n", err.Error())
			}
			err = utils.ModifyYamlFile(hulactlconfigfile, []string{"servers", authKey, "token"}, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Value: token,
			})
			if err != nil {
				fmt.Printf("Error saving token to config file: %s\n", err.Error())
			} else {
				fmt.Printf("Credentials saved for %s in %s\n", authKey, hulactlconfigfile)
			}
		}

	case CMD_CREATEFORM:
		var body string
		if doAltGetBody(hulactlconfig) {
			if hulactlconfig.GetInteractive {
				app := tview.NewApplication()
				form := tview.NewForm().
					//		AddDropDown("Title", []string{"Mr.", "Ms.", "Mrs.", "Dr.", "Prof."}, 0, nil).
					AddInputField("Form name", "Original", 50, nil, nil).
					//		AddInputField("Last name", "", 20, nil, nil).
					AddTextArea("JSON schema", "", 0, 20, 0, nil).
					AddInputField("Description (optional)", "", 100, nil, nil).
					AddInputField("Captcha (turnstile, recpatcha2, recaptcha3)", "", 50, nil, nil).
					AddTextArea("Feedback template (optiona)", "", 0, 15, 0, nil).
					// AddTextView("Notes", "This is just a demo.\nYou can enter whatever you wish.", 40, 2, true, false).
					// AddCheckbox("Age 18+", false, nil).
					// AddPasswordField("Password", "", 10, '*', nil).
					AddButton("Cancel", func() {
						fmt.Printf("Canceled.")
						os.Exit(1)
					}).
					AddButton("Done (or CTRL-C)", func() {
						app.Stop()
					})

				form.SetTitle("Enter some data").SetTitleAlign(tview.AlignLeft) //SetBorder(true)
				if err := app.SetRoot(form, true).Run(); err != nil {           // .EnableMouse(true)
					fmt.Printf("Error from terminal (tview): %s", err)
					os.Exit(1)
				}

				name := form.GetFormItem(0).(*tview.InputField).GetText()
				txt := form.GetFormItem(1).(*tview.TextArea).GetText()
				desc := form.GetFormItem(2).(*tview.InputField).GetText()
				cap := form.GetFormItem(3).(*tview.InputField).GetText()
				feedback := form.GetFormItem(4).(*tview.TextArea).GetText()
				// create body using FormModelReq
				// encode as JSON
				var fmodel handler.FormModelReq

				fmodel.Name = name
				fmodel.Description = desc
				fmodel.Schema = txt
				fmodel.Captcha = cap
				fmodel.Feedback = feedback

				var d []byte
				d, err = json.Marshal(fmodel)
				if err != nil {
					fmt.Printf("Error marshalling form model (1): %s\n", err.Error())
					os.Exit(1)
				}
				body = string(d)
			} else {
				body, err = getAltBody(hulactlconfig)
				if err != nil {
					fmt.Printf("Error getting body for request: %s\n", err.Error())
					os.Exit(1)
				}
			}
		} else {
			if len(argz) < 2 || len(argz[1]) < 1 {
				fmt.Printf("Need the form model json file.\n")
				os.Exit(1)
			}
			body = argz[1]
		}
		client := GetHulactlClient(hulactlconfig)
		err := client.FormCreate(body)
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Form created.\n")
	case CMD_MODIFYFORM:
		if len(argz) < 2 || len(argz[1]) < 1 {
			fmt.Printf("Need the form ID as first arg\n")
			getCommandUsage(command)
			os.Exit(1)
		}
		formid := argz[1]
		var body string
		if doAltGetBody(hulactlconfig) {
			if hulactlconfig.GetInteractive {
				app := tview.NewApplication()
				form := tview.NewForm().
					//		AddDropDown("Title", []string{"Mr.", "Ms.", "Mrs.", "Dr.", "Prof."}, 0, nil).
					AddInputField("Form name", "Original", 50, nil, nil).
					//		AddInputField("Last name", "", 20, nil, nil).
					AddTextArea("JSON schema", "", 0, 20, 0, nil).
					AddInputField("Description (optional)", "", 100, nil, nil).
					AddInputField("Captcha (turnstile, recpatcha2, recaptcha3)", "", 50, nil, nil).
					AddTextArea("Feedback template (optiona)", "", 0, 15, 0, nil).

					// AddTextView("Notes", "This is just a demo.\nYou can enter whatever you wish.", 40, 2, true, false).
					// AddCheckbox("Age 18+", false, nil).
					// AddPasswordField("Password", "", 10, '*', nil).
					AddButton("Cancel", func() {
						fmt.Printf("Canceled.")
						os.Exit(1)
					}).
					AddButton("Done (or CTRL-C)", func() {
						app.Stop()
					})

				form.SetTitle("Enter some data").SetTitleAlign(tview.AlignLeft) //SetBorder(true)
				if err := app.SetRoot(form, true).Run(); err != nil {           // .EnableMouse(true)
					fmt.Printf("Error from terminal (tview): %s", err)
					os.Exit(1)
				}

				name := form.GetFormItem(0).(*tview.InputField).GetText()
				txt := form.GetFormItem(1).(*tview.TextArea).GetText()
				desc := form.GetFormItem(2).(*tview.InputField).GetText()
				cap := form.GetFormItem(3).(*tview.InputField).GetText()
				feedback := form.GetFormItem(4).(*tview.TextArea).GetText()

				// create body using FormModelReq
				// encode as JSON
				var fmodel handler.FormModelReq

				fmodel.Name = name
				fmodel.Description = desc
				fmodel.Schema = txt
				fmodel.Captcha = cap
				fmodel.Feedback = feedback

				var d []byte
				d, err = json.Marshal(fmodel)
				if err != nil {
					fmt.Printf("Error marshalling form model (1): %s\n", err.Error())
					os.Exit(1)
				}
				body = string(d)
			} else {
				body, err = getAltBody(hulactlconfig)
				if err != nil {
					fmt.Printf("Error getting body for request: %s\n", err.Error())
					os.Exit(1)
				}
			}
		} else {
			if len(argz) < 2 || len(argz[1]) < 1 {
				fmt.Printf("Need the form model json file.\n")
				os.Exit(1)
			}
			body = argz[1]
		}
		client := GetHulactlClient(hulactlconfig)
		err := client.FormModify(formid, body)
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Form created.\n")

	case CMD_SUBMITFORM:
		var body string
		if len(argz) < 2 || len(argz[1]) < 1 {
			fmt.Printf("Need the form ID as first arg\n")
			getCommandUsage(command)
			os.Exit(1)
		}
		formid := argz[1]
		if doAltGetBody(hulactlconfig) {
			if hulactlconfig.GetInteractive {
				app := tview.NewApplication()
				form := tview.NewForm().
					//		AddDropDown("Title", []string{"Mr.", "Ms.", "Mrs.", "Dr.", "Prof."}, 0, nil).
					AddInputField("URL", "http://...", 50, nil, nil).
					//		AddInputField("Last name", "", 20, nil, nil).
					AddTextArea("Fields (JSON)", "", 0, 20, 0, nil).
					AddInputField("VC (cookie)", "", 100, nil, nil).
					AddInputField("Captcha", "", 100, nil, nil).
					// AddTextView("Notes", "This is just a demo.\nYou can enter whatever you wish.", 40, 2, true, false).
					// AddCheckbox("Age 18+", false, nil).
					// AddPasswordField("Password", "", 10, '*', nil).
					AddButton("Cancel", func() {
						fmt.Printf("Canceled.")
						os.Exit(1)
					}).
					AddButton("Done (or CTRL-C)", func() {
						app.Stop()
					})

				form.SetTitle("Enter form submission").SetTitleAlign(tview.AlignLeft) //SetBorder(true)
				if err := app.SetRoot(form, true).Run(); err != nil {                 // .EnableMouse(true)
					fmt.Printf("Error from terminal (tview): %s", err)
					os.Exit(1)
				}

				url := form.GetFormItem(0).(*tview.InputField).GetText()
				fields := form.GetFormItem(1).(*tview.TextArea).GetText()
				vc := form.GetFormItem(2).(*tview.InputField).GetText()
				captcha := form.GetFormItem(3).(*tview.InputField).GetText()
				// create body using FormModelReq
				// encode as JSON
				var fmodel handler.FormPostReq
				fmodel.URL = url
				fmodel.Captcha = captcha
				fmodel.Fields = make(map[string]string)
				err = json.Unmarshal([]byte(fields), &fmodel.Fields)
				if err != nil {
					fmt.Printf("Error unmarshalling fields (JSON error): %s\n", err.Error())
					os.Exit(1)
				}
				fmodel.SSCookie = vc

				var d []byte
				d, err = json.Marshal(fmodel)
				if err != nil {
					fmt.Printf("Error marshalling form submission (1): %s\n", err.Error())
					os.Exit(1)
				}
				body = string(d)
			} else {
				body, err = getAltBody(hulactlconfig)
				if err != nil {
					fmt.Printf("Error getting body for request: %s\n", err.Error())
					os.Exit(1)
				}
			}
		} else {
			if len(argz) < 3 || len(argz[2]) < 1 {
				fmt.Printf("Need the form data submission file.\n")
				os.Exit(1)
			}
			body = argz[2]
		}
		client := GetHulactlClient(hulactlconfig)
		resp, err := client.FormSubmit([]byte(body), formid, hulactlconfig.HostId)
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		if resp.StatusCode != 200 {
			fmt.Printf("Error: %d\n", resp.StatusCode)
			fmt.Printf("Body: %s\n", resp.Body)
			os.Exit(1)
		}
		fmt.Printf("Form submitted.\n")

	case CMD_CREATELANDER:
		var body string
		if doAltGetBody(hulactlconfig) {
			if hulactlconfig.GetInteractive {
				app := tview.NewApplication()
				form := tview.NewForm().
					//		AddDropDown("Title", []string{"Mr.", "Ms.", "Mrs.", "Dr.", "Prof."}, 0, nil).
					AddInputField("Lander Name", "lander1", 50, nil, nil).
					AddInputField("Server", "hostname.com", 50, nil, nil).
					AddInputField("Description (optional)", "", 50, nil, nil).
					//		AddInputField("Last name", "", 20, nil, nil).
					AddInputField("Redirect", "", 100, nil, nil).
					AddCheckbox("NoServe", false, nil).
					AddCheckbox("IgnorePort", false, nil).
					AddButton("Cancel", func() {
						fmt.Printf("Canceled.")
						os.Exit(1)
					}).
					AddButton("Done (or CTRL-C)", func() {
						app.Stop()
					})

				form.SetTitle("Enter lander info").SetTitleAlign(tview.AlignLeft) //SetBorder(true)
				if err := app.SetRoot(form, true).Run(); err != nil {             // .EnableMouse(true)
					fmt.Printf("Error from terminal (tview): %s", err)
					os.Exit(1)
				}

				name := form.GetFormItem(0).(*tview.InputField).GetText()
				server := form.GetFormItem(1).(*tview.InputField).GetText()
				desc := form.GetFormItem(2).(*tview.InputField).GetText()
				redirect := form.GetFormItem(3).(*tview.InputField).GetText()
				noserve := form.GetFormItem(4).(*tview.Checkbox).IsChecked()
				ignoreport := form.GetFormItem(5).(*tview.Checkbox).IsChecked()
				// create body using FormModelReq
				// encode as JSON
				var model handler.LanderReq

				model.Name = name
				model.Server = server
				model.Description = desc
				model.Redirect = redirect
				model.NoServe = &noserve
				model.IgnorePort = &ignoreport

				var d []byte
				d, err = json.Marshal(model)
				if err != nil {
					fmt.Printf("Error marshalling form model (1): %s\n", err.Error())
					os.Exit(1)
				}
				body = string(d)
			} else {
				body, err = getAltBody(hulactlconfig)
				if err != nil {
					fmt.Printf("Error getting body for request: %s\n", err.Error())
					os.Exit(1)
				}
			}
		} else {
			if len(argz) < 2 || len(argz[1]) < 1 {
				fmt.Printf("Need the lander model json file.\n")
				os.Exit(1)
			}
			body = argz[1]
		}
		client := GetHulactlClient(hulactlconfig)
		resp, err := client.LanderCreate(body)
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Lander created. url: %s id: %s\n", resp.FinalUrl, resp.ID)

	case "authok":
		client := GetHulactlClient(hulactlconfig)
		resp, err := client.StatusAuthOK()
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Status auth ok.\n")
		prettyout, err := json.MarshalIndent(resp.Response.(*handler.StatusAuthOKResp), "", "  ")
		if err != nil {
			fmt.Printf("Error marshalling JSON after response: %s\n", err.Error())
		} else {
			fmt.Printf("%s\n", string(prettyout))
		}
	case "createuser":

	case "deletedb":
		var hulationconf *config.Config
		if len(hulactlconfig.HulationConfigPath) > 0 {
			hulationconf, err = config.LoadConfig(confpath)

			if err != nil {
				fmt.Printf("Error loading config: (%s) %s", confpath, err.Error())
				os.Exit(1)
			}
		} else {
			fmt.Printf("Need the hulation.yaml config file.\n")
			os.Exit(1)
		}

		conn, ctx, err := setupInitConn(hulationconf, "")
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Delete database %s\n", hulationconf.DBConfig.DBName)
		fmt.Printf("DROP DATABASE IF EXISTS %s\n", hulationconf.DBConfig.DBName)
		if askForConfirmation() {
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", hulationconf.DBConfig.DBName)); err != nil {
				fmt.Printf("Error: %s\n", err.Error())
				os.Exit(1)
			} else {
				fmt.Printf("Database %s deleted if existed.\n", hulationconf.DBConfig.DBName)
			}
			conn.Close()
		}
	// case "createuser":
	// 	conn, _, err := setupInitConn(hulationconf, hulationconf.DBConfig.DBName)
	// 	if err != nil {
	// 		fmt.Printf("Error on reconnect: %s\n", err.Error())
	// 		os.Exit(1)
	// 	}

	// 	//	conn.Close()
	// 	fmt.Printf("testing gorm w/ clickhouse driver...\n")
	// 	var db *gorm.DB
	// 	if db, err = gorm.Open(clickhouse.New(clickhouse.Config{
	// 		Conn: conn,
	// 	}), &gorm.Config{}); err != nil {
	// 		fmt.Printf("failed to connect database, got error %v", err)
	// 		os.Exit(1)
	// 	}

	// 	fmt.Printf("No error. Connectivity ok.\n")
	// 	fmt.Printf("Automigrate models...\n")
	// 	model.AutoMigrateVisitorModels(db)

	// 	fmt.Printf("Automigrate done.\n")
	case "initdb":
		hulationconf := GetHulationServerConfigOrExit(hulactlconfig.HulationConfigPath)
		if len(command) > 0 {
			fmt.Printf("Unknown command: %s\n", command)
			os.Exit(1)
		}
		conn, ctx, err := setupInitConn(hulationconf, "")
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}

		fmt.Printf("Create database %s\n", hulationconf.DBConfig.DBName)
		fmt.Printf("CREATE DATABASE IF NOT EXISTS %s\n", hulationconf.DBConfig.DBName)
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", hulationconf.DBConfig.DBName)); err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		conn.Close()
		// reconnect to the database we just created
		fmt.Printf("ok. reconnecting to database %s...\n", hulationconf.DBConfig.DBName)
		conn, ctx, err = setupInitConn(hulationconf, hulationconf.DBConfig.DBName)
		if err != nil {
			fmt.Printf("Error on reconnect: %s\n", err.Error())
			os.Exit(1)
		}

		//	conn.Close()
		fmt.Printf("testing gorm w/ clickhouse driver...\n")
		var db *gorm.DB
		if db, err = gorm.Open(clickhouse.New(clickhouse.Config{
			Conn: conn,
		}), &gorm.Config{}); err != nil {
			fmt.Printf("failed to connect database, got error %v", err)
			os.Exit(1)
		}

		fmt.Printf("No error. Connectivity ok.\n")
		fmt.Printf("Automigrate models...\n")
		model.AutoMigrateVisitorModels(db)
		model.AutoMigrateFormModels(db)

		fmt.Printf("Automigrate done.\n")
	case CMD_UPDATEADMINHASH:
		if hulactlconfig.HulationConfigPath == "" {
			fmt.Printf("Need -hulaconf flag pointing to the hulation config file.\n")
			os.Exit(1)
		}
		l, err := readline.NewEx(&readline.Config{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
			os.Exit(1)
		}
		passwordb, err := l.ReadPassword("Enter new admin password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading password: %s\n", err.Error())
			os.Exit(1)
		}
		passwordb2, err := l.ReadPassword("Confirm password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading password: %s\n", err.Error())
			os.Exit(1)
		}
		if string(passwordb) != string(passwordb2) {
			fmt.Printf("Passwords do not match.\n")
			os.Exit(1)
		}
		hash, err := utils.Argon2GenerateFromSecretDefaults(string(passwordb))
		if err != nil {
			fmt.Printf("Error generating hash: %s\n", err.Error())
			os.Exit(1)
		}
		err = utils.ModifyYamlFile(hulactlconfig.HulationConfigPath, []string{"admin", "hash"}, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: hash,
		})
		if err != nil {
			fmt.Printf("Error updating config file: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Admin hash updated in %s\n", hulactlconfig.HulationConfigPath)

	case CMD_RELOAD:
		pid, err := findHulaProcess()
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			fmt.Printf("Error finding process %d: %s\n", pid, err.Error())
			os.Exit(1)
		}
		err = proc.Signal(syscall.SIGHUP)
		if err != nil {
			fmt.Printf("Error sending SIGHUP to pid %d: %s\n", pid, err.Error())
			os.Exit(1)
		}
		fmt.Printf("Sent SIGHUP to hula (pid %d) — config reload triggered\n", pid)

	case CMD_TOTPKEY:
		key, err := utils.GenerateTOTPEncryptionKey()
		if err != nil {
			fmt.Printf("Error generating key: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("TOTP encryption key: %s\n", key)
		fmt.Printf("\nAdd to your hulation config:\n  totp_encryption_key: \"%s\"\n", key)

	case CMD_OPAQUESEED:
		// Generate fresh OPAQUE OPRF seed + AKE secret, both
		// base64url-encoded. Operator pastes into config or env.
		seed := hulaopaque.GenerateSeedB64()
		akeSecret, err := hulaopaque.GenerateAKESecretB64()
		if err != nil {
			fmt.Printf("Error generating AKE secret: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("OPAQUE OPRF seed:  %s\n", seed)
		fmt.Printf("OPAQUE AKE secret: %s\n", akeSecret)
		fmt.Printf("\nAdd to your hulation config (or set env):\n")
		fmt.Printf("  opaque:\n")
		fmt.Printf("    oprf_seed: \"%s\"\n", seed)
		fmt.Printf("    ake_secret: \"%s\"\n", akeSecret)
		fmt.Printf("\nOR via env vars:\n")
		fmt.Printf("  HULA_OPAQUE_OPRF_SEED=\"%s\"\n", seed)
		fmt.Printf("  HULA_OPAQUE_AKE_SECRET=\"%s\"\n", akeSecret)
		fmt.Printf("\nIMPORTANT: changing these invalidates every existing OPAQUE record.\n")

	case CMD_FORGETOPAQUE:
		// EMERGENCY recovery: delete an OPAQUE record directly from
		// a Bolt file. hula MUST be stopped first (Bolt allows one
		// process at a time). Used by reset-admin-password.sh after
		// `docker cp`-ing the bolt out of the container.
		if hulactlconfig.BoltPath == "" {
			fmt.Fprintf(os.Stderr, "Error: --bolt <path> is required\n")
			fmt.Fprintf(os.Stderr, "Usage: %s\n", CMD_FORGETOPAQUE_USAGE)
			os.Exit(1)
		}
		if len(argz) < 3 {
			fmt.Fprintf(os.Stderr, "Error: provider and username are required\n")
			fmt.Fprintf(os.Stderr, "Usage: %s\n", CMD_FORGETOPAQUE_USAGE)
			os.Exit(1)
		}
		provider := argz[1]
		username := argz[2]
		// Open the Bolt file directly (no hulabolt.Open process global —
		// this is a one-shot edit and we want crisp control over
		// open/close).
		db, err := bolt.Open(hulactlconfig.BoltPath, 0o600, &bolt.Options{Timeout: 5 * time.Second})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening bolt: %s\n", err.Error())
			os.Exit(1)
		}
		key := provider + "|" + username
		err = db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte("opaque_records"))
			if b == nil {
				return fmt.Errorf("opaque_records bucket missing")
			}
			if v := b.Get([]byte(key)); v == nil {
				fmt.Printf("(no record for %s — already absent)\n", key)
				return nil
			}
			if err := b.Delete([]byte(key)); err != nil {
				return err
			}
			fmt.Printf("Deleted opaque_records[%s]\n", key)
			return nil
		})
		if cerr := db.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
			os.Exit(1)
		}

	case CMD_ROTATECOOKIELESS:
		// Phase 4c.3: replace the per-server cookieless visitor-id
		// salt. Same offline-edit pattern as forget-opaque-record:
		// hula must be stopped, caller copies the bolt out, runs
		// this, copies it back.
		if hulactlconfig.BoltPath == "" {
			fmt.Fprintf(os.Stderr, "Error: --bolt <path> is required\n")
			fmt.Fprintf(os.Stderr, "Usage: %s\n", CMD_ROTATECOOKIELESS_USAGE)
			os.Exit(1)
		}
		if len(argz) < 2 {
			fmt.Fprintf(os.Stderr, "Error: server_id is required\n")
			fmt.Fprintf(os.Stderr, "Usage: %s\n", CMD_ROTATECOOKIELESS_USAGE)
			os.Exit(1)
		}
		serverID := argz[1]
		db, err := bolt.Open(hulactlconfig.BoltPath, 0o600, &bolt.Options{Timeout: 5 * time.Second})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening bolt: %s\n", err.Error())
			os.Exit(1)
		}
		fresh := make([]byte, 32)
		if _, err := cryptorand.Read(fresh); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading random: %s\n", err.Error())
			_ = db.Close()
			os.Exit(1)
		}
		err = db.Update(func(tx *bolt.Tx) error {
			b, berr := tx.CreateBucketIfNotExists([]byte("cookieless_salts"))
			if berr != nil {
				return berr
			}
			return b.Put([]byte(serverID), fresh)
		})
		if cerr := db.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Rotated cookieless_salts[%s] (32 random bytes)\n", serverID)

	case CMD_SETPASSWORD:
		// Always require fresh proof of the current password for
		// rotation. We deliberately ignore any saved JWT in
		// hulactl.yaml — a leaked or replayed JWT must NOT be enough
		// to rotate the password.
		username := hulactlconfig.SetPasswordUser
		if username == "" {
			username = "admin"
		}
		provider := hulactlconfig.SetPasswordProvider
		if provider == "" {
			provider = "admin"
		}

		// 1) Read the CURRENT password (or empty for first-time
		//    bootstrap). Env preferred over interactive. We must
		//    distinguish "env unset" (prompt) from "env set to
		//    empty string" (intentional bootstrap, do NOT prompt) —
		//    the latter is how wrapper scripts signal bootstrap mode.
		var currentPass string
		curFromEnv := false
		if v, ok := os.LookupEnv("HULACTL_CURRENT_PASSWORD"); ok {
			currentPass = v
			curFromEnv = true
		}

		// 2) Read the NEW password (env preferred over interactive).
		var newPass string
		if hulactlconfig.NewPassword != "" {
			newPass = hulactlconfig.NewPassword
		}

		// 3) If anything is still missing, prompt interactively.
		if !curFromEnv || newPass == "" {
			l, err := readline.NewEx(&readline.Config{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
				os.Exit(1)
			}
			if !curFromEnv {
				cur, err := l.ReadPassword(fmt.Sprintf(
					"Current password for %s/%s (leave blank ONLY for first-time bootstrap): ",
					provider, username))
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading password: %s\n", err.Error())
					os.Exit(1)
				}
				currentPass = string(cur)
			}
			if newPass == "" {
				pw1, err := l.ReadPassword(fmt.Sprintf("New password for %s/%s: ", provider, username))
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading password: %s\n", err.Error())
					os.Exit(1)
				}
				pw2, err := l.ReadPassword("Confirm new password: ")
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading password: %s\n", err.Error())
					os.Exit(1)
				}
				if string(pw1) != string(pw2) {
					fmt.Printf("Passwords don't match.\n")
					os.Exit(1)
				}
				if len(pw1) < 8 {
					fmt.Printf("New password too short (min 8 chars).\n")
					os.Exit(1)
				}
				newPass = string(pw1)
			}
		}

		// 4) Build a FRESH client with no saved token. We must not
		//    reuse a JWT from hulactl.yaml — rotation requires
		//    just-now proof of the current password.
		url, _, insecure, rerr := resolveServer(hulactlconfig)
		if rerr != nil {
			fmt.Printf("Error: %s\n", rerr)
			os.Exit(1)
		}
		c := client.NewClient(url, "")
		if insecure {
			c.SetInsecure(true)
		}

		// 5) Rotation path: log in with the current password. The
		//    server returns NotFound (404) when no record exists yet
		//    — that's the bootstrap case; we proceed without auth.
		if currentPass != "" {
			loginRes, lerr := c.OpaqueLogin(provider, username, currentPass)
			if lerr != nil {
				lmsg := lerr.Error()
				switch {
				case strings.Contains(lmsg, "HTTP 404"):
					// No record on the server. The operator typed
					// a current password but there's nothing to
					// verify against — treat as bootstrap.
					fmt.Printf("No OPAQUE record exists yet — proceeding with bootstrap (current-password ignored).\n")
				case strings.Contains(lmsg, "GenerateKE3") ||
					strings.Contains(lmsg, "decrypted server public key") ||
					strings.Contains(lmsg, "ristretto255: invalid element"):
					// Wrong-password failure mode. All three of these
					// strings come from bytemare/opaque's KE2-decrypt
					// path when the password-derived envelope key
					// produces garbage when applied to the server's
					// record. The operator sees the same outcome
					// either way: "your password is wrong".
					fmt.Printf("Error: incorrect current password.\n")
					if hulactlconfig.DebugMode {
						fmt.Fprintf(os.Stderr, "  (bytemare diagnostic: %s)\n", lmsg)
					}
					os.Exit(1)
				default:
					// Any other failure (network, server 500, etc.)
					// — surface verbatim so the operator can triage.
					fmt.Printf("Error: could not verify current password: %s\n", lmsg)
					os.Exit(1)
				}
			} else {
				c.SetToken(loginRes.JWT)
			}
		}

		// 6) Register the new password. Bearer is now either
		//    a fresh JWT (rotation) or empty (bootstrap).
		if err := c.OpaqueRegister(provider, username, newPass); err != nil {
			msg := err.Error()
			fmt.Printf("Error: %s\n", msg)
			if strings.Contains(msg, "OPAQUE register requires admin authentication") {
				fmt.Printf("\nThis user already has an OPAQUE record on the server,\n")
				fmt.Printf("so rotation requires the current password to verify.\n")
				fmt.Printf("Re-run set-password and provide the current password when prompted\n")
				fmt.Printf("(or set HULACTL_CURRENT_PASSWORD).\n")
			}
			os.Exit(1)
		}
		fmt.Printf("Password for %s/%s set via OPAQUE.\n", provider, username)
		fmt.Printf("The password itself was never sent to the server.\n")

	case CMD_TOTPSETUP:
		client := GetHulactlClient(hulactlconfig)
		// Step 1: Call setup endpoint
		setupResp, err := client.TotpSetup()
		if err != nil {
			fmt.Printf("Error setting up TOTP: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("TOTP Setup\n")
		fmt.Printf("  Secret: %s\n", setupResp.Secret)
		fmt.Printf("  URL (for QR code): %s\n", setupResp.URL)
		fmt.Printf("\n  Recovery codes (save these!):\n")
		for i, code := range setupResp.RecoveryCodes {
			fmt.Printf("    %d. %s\n", i+1, code)
		}
		fmt.Printf("\nEnter the code from your authenticator app to complete setup:\n")
		l, err := readline.NewEx(&readline.Config{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
			os.Exit(1)
		}
		l.SetPrompt("TOTP Code: ")
		code, err := l.Readline()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading code: %s\n", err.Error())
			os.Exit(1)
		}
		err = client.TotpVerifySetup(strings.TrimSpace(code))
		if err != nil {
			fmt.Printf("Error verifying TOTP: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("TOTP enabled successfully.\n")

	case CMD_BADACTORS:
		client := GetHulactlClient(hulactlconfig)
		// Get stats first for threshold info
		stats, err := client.BadActorStats()
		if err != nil {
			fmt.Printf("Error getting bad actor stats: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Bad Actor Status: enabled=%v  dry_run=%v  threshold=%d  ttl=%s\n",
			stats.Enabled, stats.DryRun, stats.BlockThreshold, stats.TTL)
		fmt.Printf("Blocked IPs: %d  Allowlisted: %d  Signatures: %d\n\n",
			stats.BlockedIPs, stats.AllowlistedIPs, stats.Signatures)

		entries, err := client.BadActorList()
		if err != nil {
			fmt.Printf("Error getting bad actor list: %s\n", err.Error())
			os.Exit(1)
		}
		if len(entries) == 0 {
			fmt.Printf("No bad actors detected.\n")
		} else {
			fmt.Printf("%-18s %-7s %-9s %-22s %-22s %s\n",
				"IP", "SCORE", "STATUS", "DETECTED", "EXPIRES", "REASON")
			fmt.Printf("%-18s %-7s %-9s %-22s %-22s %s\n",
				"--", "-----", "------", "--------", "-------", "------")
			for _, e := range entries {
				status := "flagged"
				if e.Blocked {
					status = "BLOCKED"
				}
				fmt.Printf("%-18s %-7d %-9s %-22s %-22s %s\n",
					e.IP,
					e.Score,
					status,
					e.DetectedAt.Format("2006-01-02 15:04:05"),
					e.ExpiresAt.Format("2006-01-02 15:04:05"),
					e.LastReason,
				)
			}
			fmt.Printf("\n%d entries total\n", len(entries))
		}

	case CMD_BUILDSITE:
		if len(argz) < 2 {
			fmt.Printf("Usage: hulactl build <server-id>\n")
			os.Exit(1)
		}
		serverID := argz[1]
		client := GetHulactlClient(hulactlconfig)
		_, result, err := client.TriggerBuild(serverID)
		if err != nil {
			fmt.Printf("Error triggering build: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Build triggered: %s\n", result.BuildID)
		fmt.Printf("Polling for completion...\n")

		// Poll until complete or failed
		for {
			time.Sleep(2 * time.Second)
			_, status, err := client.BuildStatus(result.BuildID)
			if err != nil {
				fmt.Printf("Error checking status: %s\n", err.Error())
				os.Exit(1)
			}
			fmt.Printf("  [%s] %s\n", status.StatusText, lastLog(status.Logs))
			if status.StatusText == "complete" {
				fmt.Printf("\nBuild complete!\n")
				break
			}
			if status.StatusText == "failed" {
				fmt.Printf("\nBuild failed: %s\n", status.Error)
				os.Exit(1)
			}
		}

	case CMD_BUILDSTATUS:
		if len(argz) < 2 {
			fmt.Printf("Usage: hulactl build-status <build-id>\n")
			os.Exit(1)
		}
		client := GetHulactlClient(hulactlconfig)
		_, status, err := client.BuildStatus(argz[1])
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Build:   %s\n", status.BuildID)
		fmt.Printf("Server:  %s\n", status.ServerID)
		fmt.Printf("Status:  %s\n", status.StatusText)
		fmt.Printf("Started: %s\n", status.StartedAt)
		if status.EndedAt != nil {
			fmt.Printf("Ended:   %s\n", *status.EndedAt)
		}
		if status.Error != "" {
			fmt.Printf("Error:   %s\n", status.Error)
		}
		if len(status.Logs) > 0 {
			fmt.Printf("\nLogs:\n")
			for _, l := range status.Logs {
				fmt.Printf("  %s\n", l)
			}
		}

	case CMD_BUILDS:
		if len(argz) < 2 {
			fmt.Printf("Usage: hulactl builds <server-id>\n")
			os.Exit(1)
		}
		client := GetHulactlClient(hulactlconfig)
		_, builds, err := client.ListBuilds(argz[1])
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}
		if len(builds) == 0 {
			fmt.Printf("No builds found for server %s\n", argz[1])
		} else {
			fmt.Printf("%-38s %-12s %-22s %s\n", "BUILD ID", "STATUS", "STARTED", "ERROR")
			fmt.Printf("%-38s %-12s %-22s %s\n", "--------", "------", "-------", "-----")
			for _, b := range builds {
				errStr := ""
				if b.Error != "" {
					if len(b.Error) > 40 {
						errStr = b.Error[:40] + "..."
					} else {
						errStr = b.Error
					}
				}
				fmt.Printf("%-38s %-12s %-22s %s\n", b.BuildID, b.StatusText, b.StartedAt, errStr)
			}
		}

	case CMD_STAGING_BUILD:
		if len(argz) < 2 {
			fmt.Printf("Usage: hulactl staging-build <server-id>\n")
			os.Exit(1)
		}
		serverID := argz[1]
		client := GetHulactlClient(hulactlconfig)
		_, result, err := client.StagingBuild(serverID)
		if err != nil {
			fmt.Printf("Error triggering staging build: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Staging build %s: %s\n", result.Status, result.BuildID)
		if len(result.Logs) > 0 {
			fmt.Printf("\nLogs:\n")
			for _, l := range result.Logs {
				fmt.Printf("  %s\n", l)
			}
		}
		if result.Error != "" {
			fmt.Printf("\nError: %s\n", result.Error)
			os.Exit(1)
		}

	case CMD_STAGING_UPDATE:
		if len(argz) < 4 {
			fmt.Printf("Usage: hulactl staging-update <server-id> <local-file> <remote-path>\n")
			os.Exit(1)
		}
		serverID := argz[1]
		localFile := argz[2]
		remotePath := argz[3]
		client := GetHulactlClient(hulactlconfig)
		err := client.StagingUpload(serverID, localFile, remotePath)
		if err != nil {
			fmt.Printf("Error uploading file: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Uploaded %s to %s on server %s\n", localFile, remotePath, serverID)

	case CMD_STAGING_MOUNT:
		// Go's flag.Parse stops at the first non-flag arg, so `--autobuild`
		// and `--dangerous` placed AFTER `staging-mount` aren't parsed as
		// flags. Scan argz manually to pick them up wherever they appear.
		positional := []string{}
		for _, a := range argz {
			switch a {
			case "--autobuild", "-autobuild":
				hulactlconfig.AutoBuild = true
			case "--dangerous", "-dangerous":
				hulactlconfig.Dangerous = true
			default:
				positional = append(positional, a)
			}
		}
		if len(positional) < 3 {
			fmt.Printf("Usage: hulactl staging-mount <server-id> <folder-mount-point> [--autobuild] [--dangerous]\n")
			os.Exit(1)
		}
		serverID := positional[1]
		localDir := positional[2]

		absDir, err := filepath.Abs(localDir)
		if err != nil {
			fmt.Printf("Error resolving path: %s\n", err.Error())
			os.Exit(1)
		}

		hulaclient := GetHulactlClient(hulactlconfig)

		// Verify auth before starting long-running process
		_, err = hulaclient.StatusAuthOK()
		if err != nil {
			fmt.Printf("Authentication failed: %s\n", err.Error())
			os.Exit(1)
		}

		// Signal handling for clean shutdown
		ctx, cancel := context.WithCancel(context.Background())
		sigchan := make(chan os.Signal, 1)
		signal.Notify(sigchan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigchan
			fmt.Printf("\nReceived %s, shutting down...\n", sig)
			cancel()
		}()

		fmt.Printf("Mounting server %s at %s...\n", serverID, absDir)

		mounter, err := client.NewStagingMounter(ctx, hulaclient, client.StagingMountOptions{
			ServerID:    serverID,
			LocalDir:    absDir,
			Dangerous:   hulactlconfig.Dangerous,
			AutoBuild:   hulactlconfig.AutoBuild,
			Output:      fmt.Printf,
			ConfirmFunc: askForConfirmation,
		})
		if err != nil {
			fmt.Printf("Error initializing mount: %s\n", err.Error())
			os.Exit(1)
		}
		defer mounter.Close()

		if err := mounter.InitialSync(); err != nil {
			fmt.Printf("Error during initial sync: %s\n", err.Error())
			os.Exit(1)
		}

		fmt.Printf("Watching %s for changes (CTRL-C to stop)...\n", absDir)

		if err := mounter.Watch(); err != nil && ctx.Err() == nil {
			fmt.Printf("Error during watch: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Mount stopped.\n")

	default:
		fmt.Printf("Unknown command: %s\n", command)
		fmt.Printf("Usage: %s <configfile> <command>\n", os.Args[0])
		printHelp()
	}

}
