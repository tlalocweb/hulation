package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	// "gorm.io/driver/clickhouse"

	// _ "github.com/mailru/go-clickhouse"
	chapi "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/chzyer/readline"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"

	"github.com/tlalocweb/hulation/client"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/utils"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"

	"github.com/IzumaNetworks/conftagz"
)

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

const DEFAULT_CONFIG_FILE = "/etc/hulation/hulactl.yaml"

var hulactlconfigfile string

type HulactlConfig struct {
	LogLevel           string `yaml:"loglevel" flag:"loglevel" usage:"sets log level to info, warn, error, fatal, panic, debug, trace, none" default:"warn" env:"HULACTL_LOGLEVEL"`
	HulationApiUrl     string `yaml:"hulaurl" flag:"hulaapi" usage:"url to hulation api" default:"http://localhost:8080" test:"~http[s]?\\:\\/\\/[^\\/]+.*" env:"HULA_API_URL"`
	HulationConfigPath string `yaml:"hulaconf" flag:"hulaconf" usage:"path to hulation config file" default:"/etc/hulation/hulation.yaml" env:"HULA_CONF"`
	DontSaveAuth       bool   `flag:"nosaveauth" usage:"do not save the auth token to the config file"`
	Token              string `yaml:"token" flag:"token" usage:"authorization"`
	DebugMode          bool   `yaml:"debug" flag:"debug" usage:"debug mode"`
	ANSIColors         bool   `yaml:"colors" flag:"colors" usage:"use ANSI colors"`
	GetBodyFromFile    string `flag:"bodyfile" usage:"get body from file"`
	GetBodyFromStdin   bool   `flag:"bodystdin" usage:"get body from stdin"`
	GetInteractive     bool   `flag:"inter" usage:"get body from the terminal interactively"`                      // uses readline
	HostId             string `yaml:"hostid" flag:"hostid" usage:"hulation host id" default:"" env:"HULA_HOST_ID"` // needed in certain requests that emulate a visitor
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
	fmt.Printf("Processed: %v\n", processed.GetFlagsFound())
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
	c = client.NewClient(hulactlconfig.HulationApiUrl, hulactlconfig.Token)
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

	fmt.Printf("Command: %s\n", command)

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
		var hash, shasum string
		hash, shasum, err = utils.GenerateHulaHashFromPlaintextPass(password)
		if err != nil {
			fmt.Printf("Error generating hash: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Hash: %s\n", hash)
		// verify the hash works
		var match bool
		match, err = utils.Argon2CompareHashAndSecret(shasum, hash)
		if err != nil {
			fmt.Printf("Error verifying hash: %s\n", err.Error())
			os.Exit(1)
		}
		if match {
			fmt.Printf("Hash verified.\n")
		} else {
			fmt.Printf("ERROR: Hash verification failed.\n")
		}
	case CMD_AUTH:
		fmt.Printf("Generate Authorization header bearer token:\n")
		l, err := readline.NewEx(&readline.Config{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error with readline: %s\n", err.Error())
			os.Exit(1)
		}
		var identity, password string
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
		// make request against the server using /login
		// client := client.NewClient(hulactlconfig.HulationApiUrl, "")
		// if hulactlconfig.DebugMode {
		// 	client.Noisy = true
		// 	client.NoisyErr = true
		// }
		client := GetHulactlClient(hulactlconfig)
		_, token, err := client.Auth(identity, password)
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			os.Exit(1)
		}

		fmt.Printf("Token: %s\n", token)
		if !hulactlconfig.DontSaveAuth {
			// check if a file exists
			_, err = os.Stat(hulactlconfigfile)
			if err != nil {
				if os.IsNotExist(err) {
					// create the file
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
			// save the token to the config file
			err = utils.ModifyYamlFile(hulactlconfigfile, []string{"token"}, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Value: token,
			})
			if err != nil {
				fmt.Printf("Error saving token to config file (%s): %s\n", hulactlconfigfile, err.Error())
			} else {
				fmt.Printf("Token saved to config file (%s)\n", hulactlconfigfile)
			}
		}

	case "createform":
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
	default:
		fmt.Printf("Unknown command: %s\n", command)
		fmt.Printf("Usage: %s <configfile> <command>\n", os.Args[0])
		printHelp()
	}

}
