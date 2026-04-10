package app

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/rs/zerolog"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

const (
	defaultLogLevel = zerolog.ErrorLevel
)

var appConfig *config.Config
var appConfigFile string
var appDebugLevel int
var jsonLogs bool

type AppRuntimeOpts struct {
	// log DB if at debug level?
	NoDebugDbLog     bool
	NoLogVisits      bool
	NoLogErrorVisits bool
	JsonLog          bool
}

var appRuntimeOpts AppRuntimeOpts

func GetAppRuntimeOpts() *AppRuntimeOpts {
	return &appRuntimeOpts
}

func ParseFlags() {
	loglevel := flag.String("loglevel", "info", "sets log level to info, warn, error, fatal, panic, debug, trace, none")
	version := flag.Bool("version", false, "prints version")
	logopts := flag.String("logopts", "", "sets log options")
	debuglevel := flag.Int("debug", 0, "sets log level to debug")
	configfile := flag.String("config", "config.yaml", "config file to use")
	logtags := flag.String("logtags", "", "comma-separated list of log tags to enable")
	nologtags := flag.String("nologtags", "", "comma-separated list of log tags to exclude")
	//	jsonlogs := flag.Bool("J", false, "use JSON logs")

	flag.Parse()

	if *version {
		DumpVersion()
		os.Exit(0)
	}

	if logopts != nil {
		splitLogOpts := strings.Split(*logopts, ",")
		for _, opt := range splitLogOpts {
			switch opt {
			case "json":
				jsonLogs = true
				appRuntimeOpts.JsonLog = true
			case "nodebugdb":
				appRuntimeOpts.NoDebugDbLog = true
			case "nologvisits":
				appRuntimeOpts.NoLogVisits = true
			case "nologerrorvisits":
				fmt.Printf("No log error visits NOT IMPLEMENTED\n")
				appRuntimeOpts.NoLogErrorVisits = true

				// case "nocolor":
				// 	log.UseColor(false)
				// case "color":
				// 	log.UseColor(true)
				// case "nocaller":
				// 	log.UseCaller(false)
				// case "caller":
				// 	log.UseCaller(true)
				// case "nocontext":
				// 	log.UseContext(false)
				// case "context":
				// 	log.UseContext(true)
			}
		}
	}
	if configfile != nil {
		appConfigFile = *configfile
	}
	if jsonLogs {
		log.UseJsonLogs()
	}
	logLevel := defaultLogLevel
	if loglevel != nil {
		switch *loglevel {
		case "info":
			logLevel = zerolog.InfoLevel
		case "warn":
			logLevel = zerolog.WarnLevel
		case "error":
			logLevel = zerolog.ErrorLevel
		case "fatal":
			logLevel = zerolog.FatalLevel
		case "panic":
			logLevel = zerolog.PanicLevel
		case "debug":
			logLevel = zerolog.DebugLevel
		case "trace":
			logLevel = zerolog.TraceLevel
		case "none":
			logLevel = zerolog.Disabled
		}
	}
	if debuglevel != nil {
		fmt.Printf("Debug level: %d\n", *debuglevel)
		appDebugLevel = *debuglevel
		if appDebugLevel > 0 {
			if logLevel > zerolog.DebugLevel {
				logLevel = zerolog.DebugLevel
			}
		}
		if appDebugLevel > 1 {
			if logLevel > zerolog.TraceLevel {
				logLevel = zerolog.TraceLevel
			}
		}
	}
	log.SetLevel(logLevel)

	// Apply tag filters from CLI flags
	if logtags != nil && *logtags != "" {
		if err := log.SetTagFilterFromString(*logtags); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid log tags: %s\n", err.Error())
		}
	}
	if nologtags != nil && *nologtags != "" {
		if err := log.SetTagBlockFilterFromString(*nologtags); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid no-log tags: %s\n", err.Error())
		}
	}
}
func DumpVersion() {
	if config.Version != "" {
		fmt.Println(config.Version)
		return
	}
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		fmt.Println(buildInfo.Main.Version)
		return
	}
	fmt.Println("(unknown)")
}

// ReloadConfig re-reads the config file from disk and swaps the global config pointer.
// Returns the old config so callers can compare fields.
func ReloadConfig() (oldConf *config.Config, err error) {
	newConf, err := config.LoadConfig(appConfigFile)
	if err != nil {
		err = fmt.Errorf("reload config: %w", err)
		return
	}
	oldConf = appConfig
	appConfig = newConf
	return
}

// ApplyLogTagConfig applies log tag filters from the config file.
// CLI flags take precedence over config values.
func ApplyLogTagConfig() {
	if appConfig == nil {
		return
	}
	if log.GetTagFilter() == 0 && appConfig.LogTags != "" {
		if err := log.SetTagFilterFromString(appConfig.LogTags); err != nil {
			log.Warnf("Invalid log_tags in config: %s", err.Error())
		}
	}
	if appConfig.NoLogTags != "" {
		if err := log.SetTagBlockFilterFromString(appConfig.NoLogTags); err != nil {
			log.Warnf("Invalid no_log_tags in config: %s", err.Error())
		}
	}
}

func GetConfigPath() string {
	return appConfigFile
}

func GetHulaOriginHost() string {
	return appConfig.HulaHost
}

func GetHulaOriginBaseUrl() string {
	return appConfig.GetHulaServer().GetExternalUrl()
}

func LoadConfig() (err error) {
	appConfig, err = config.LoadConfig(appConfigFile)
	return err
}

// only used in test harness
func LoadConfigWithFile(configfile string) (configret *config.Config, err error) {
	appConfig, err = config.LoadConfig(configfile)
	if err != nil {
		return nil, err
	}
	configret = appConfig
	return
}

func GetLogLevel() int {
	return appDebugLevel
}

func GetAppDebugLevel() int {
	return appDebugLevel
}

func GetConfig() *config.Config {
	return appConfig
}

func ConnectToDB() {

}
