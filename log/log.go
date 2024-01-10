package log

import (
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func Init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
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
