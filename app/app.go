package app

import (
	"flag"
	"fmt"
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
	LogAllVisits     bool
	NoLogErrorVisits bool
}

var appRuntimeOpts AppRuntimeOpts

func GetAppRuntimeOpts() *AppRuntimeOpts {
	return &appRuntimeOpts
}

func ParseFlags() {
	loglevel := flag.String("loglevel", "info", "sets log level to info, warn, error, fatal, panic, debug, trace, none")
	logopts := flag.String("logopts", "", "sets log options")
	debuglevel := flag.Int("debug", 0, "sets log level to debug")
	configfile := flag.String("config", "config.yaml", "config file to use")
	jsonlogs := flag.Bool("J", false, "use JSON logs")

	flag.Parse()
	if logopts != nil {
		splitLogOpts := strings.Split(*logopts, ",")
		for _, opt := range splitLogOpts {
			switch opt {
			case "nodebugdb":
				appRuntimeOpts.NoDebugDbLog = true
			case "logallvisits":
				appRuntimeOpts.LogAllVisits = true
			case "nologerrorvisits":
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
	jsonLogs = *jsonlogs
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

}

func LoadConfig() (err error) {
	appConfig, err = config.LoadConfig(appConfigFile)
	return err
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
