package log

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

var log zerolog.Logger

func init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	//	logger = log
	//logger = zerolog.New(os.Stderr).With().Timestamp().Logger()

	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.TimeOnly, NoColor: false}
	// output.FormatLevel = func(i interface{}) string {
	// 	return strings.ToUpper(fmt.Sprintf("| %-6s|", i))
	// }
	// output.FormatMessage = func(i interface{}) string {
	// 	return fmt.Sprintf("***%s****", i)
	// }
	// output.FormatFieldName = func(i interface{}) string {
	// 	return fmt.Sprintf("%s:", i)
	// }
	// output.FormatFieldValue = func(i interface{}) string {
	// 	return strings.ToUpper(fmt.Sprintf("%s", i))
	// }
	log = zerolog.New(output).With().Timestamp().Logger()

	// log.Info().Str("foo", "bar").Msg("Hello World")
}

func UseJsonLogs() {
	log = zerolog.New(os.Stdout).With().Timestamp().Logger()
}

func SetLevel(level zerolog.Level) {
	log = log.Level(level)
}

func GetLogger() *zerolog.Logger {
	return &log
}

func Debugf(str string, args ...interface{}) {
	log.Debug().Msgf(str, args...)
}

func Infof(str string, args ...interface{}) {
	log.Info().Msgf(str, args...)
}

func Warnf(str string, args ...interface{}) {
	log.Warn().Msgf(str, args...)
}

func Errorf(str string, args ...interface{}) {
	log.Error().Msgf(str, args...)
}

func Fatalf(str string, args ...interface{}) {
	log.Fatal().Msgf(str, args...)
}

func Panicf(str string, args ...interface{}) {
	log.Panic().Msgf(str, args...)
}

func Tracef(str string, args ...interface{}) {
	log.Trace().Msgf(str, args...)
}

type LogLevel int
