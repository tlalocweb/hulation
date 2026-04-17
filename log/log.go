package log

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

var lastTag uint64 = 0x1
var numTags int

var tagRegistry map[string]uint64
var reverseTagRegistry map[uint64]string
var tagLoggers map[uint64]*TaggedLogger

// the default debug verbosity is 0. More detailed logging is 1 or 2
var debugVerbosity int

// these tags do get logged
var tagFilter uint64

// a inverse filter - these tags don't get logged
var tagBlockFilter uint64

func init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.TimeOnly, NoColor: false}
	log = zerolog.New(output).With().Timestamp().Logger()

	tagRegistry = make(map[string]uint64)
	reverseTagRegistry = make(map[uint64]string)
	tagLoggers = make(map[uint64]*TaggedLogger)
}

func MaskSecret(s string) string {
	return strings.Repeat("*", len(s))
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

const (
	DebugLevel = zerolog.DebugLevel
	InfoLevel  = zerolog.InfoLevel
	WarnLevel  = zerolog.WarnLevel
	ErrorLevel = zerolog.ErrorLevel
	FatalLevel = zerolog.FatalLevel
	PanicLevel = zerolog.PanicLevel
)

var globalLogLevelLogr int

func SetLevelByString(level string) (err error) {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "debug":
		log = log.Level(zerolog.DebugLevel)
		globalLogLevelLogr = 4
	case "info":
		log = log.Level(zerolog.InfoLevel)
		globalLogLevelLogr = 3
	case "warn":
		log = log.Level(zerolog.WarnLevel)
		globalLogLevelLogr = 2
	case "error":
		log = log.Level(zerolog.ErrorLevel)
		globalLogLevelLogr = 1
	case "fatal":
		log = log.Level(zerolog.FatalLevel)
		globalLogLevelLogr = 0
	case "panic":
		log = log.Level(zerolog.PanicLevel)
		globalLogLevelLogr = 0
	default:
		err = fmt.Errorf("invalid log level: %s", level)
	}
	return
}

func SetDebugVerbosity(verbosity int) {
	debugVerbosity = verbosity
}

func SetTagFilter(tag uint64) {
	tagFilter = tag
}

// accepts a comma separated string of valid tag names
func SetTagFilterFromString(tag string) (err error) {
	if len(tag) == 0 {
		return
	}
	tagFilter = 0
	tagz := strings.Split(tag, ",")
	for _, v := range tagz {
		val, ok := tagRegistry[v]
		if ok {
			tagFilter = tagFilter | val
		} else {
			err = fmt.Errorf("invalid tag: %s", v)
			return
		}
	}
	return
}

// accepts a comma separated string of valid tag names
func SetTagBlockFilterFromString(tag string) (err error) {
	if len(tag) == 0 {
		return
	}
	tagBlockFilter = 0
	tagz := strings.Split(tag, ",")
	for _, v := range tagz {
		val, ok := tagRegistry[v]
		if ok {
			tagBlockFilter = tagBlockFilter | val
		} else {
			err = fmt.Errorf("invalid tag: %s", v)
			return
		}
	}
	return
}

func GetTagFilter() uint64 {
	return tagFilter
}

func GetValidTags() []string {
	ret := make([]string, numTags)
	for _, v := range reverseTagRegistry {
		ret = append(ret, v)
	}
	return ret
}

func GetTagLogger(tag uint64) *TaggedLogger {
	return tagLoggers[tag]
}

func GetTagLoggers() map[uint64]*TaggedLogger {
	return tagLoggers
}

func GetValidTagsCommaSeparated() string {
	ret := make([]string, 0, numTags)
	for _, v := range reverseTagRegistry {
		ret = append(ret, v)
	}
	return strings.Join(ret, ",")
}

func GetTag(name string) uint64 {
	return tagRegistry[name]
}

type TaggedLogger struct {
	Sublogger
	tag         uint64
	Description string
	Name        string
}

func GetTaggedLogger(name string, description string) *TaggedLogger {
	if numTags > 64 {
		panic("too many log tags")
	}
	existingTag, ok := tagRegistry[name]
	if ok {
		Debugf("Note: GetTaggedLogger() called for existing tag: %s @loc %s", name, Loc(Folders(2), StackLevel(2)))
		return tagLoggers[existingTag]
	}
	lastTag = lastTag << 1
	ret := &TaggedLogger{
		Name:        name,
		Description: description,
		Sublogger: Sublogger{
			prefix:      name + ": ",
			logger:      log,
			codeContext: true,
		},
		tag: lastTag,
	}
	tagRegistry[name] = lastTag
	reverseTagRegistry[lastTag] = name
	tagLoggers[lastTag] = ret
	numTags++
	return ret
}

func (s *TaggedLogger) Loc(opts ...FLOpts) string {
	return Loc(opts...)
}

func (s *TaggedLogger) StackLevel(level int) FLOpts {
	return StackLevel(level)
}

func (s *TaggedLogger) WrapAndLogError(format string, err error) error {
	return WrapAndLogError(format, err)
}

// Verbose Debugf
func (s *TaggedLogger) VDebugf(verbosity int, str string, args ...interface{}) {
	if debugVerbosity >= verbosity {
		var vinfo string
		if debugVerbosity >= 0 {
			vinfo = fmt.Sprintf(" [v%d] ", verbosity)
		}
		if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
			if s.tag&tagBlockFilter == 0 {
				caller := s.CodeCallString()
				s.logger.Debug().Msgf(s.prefix+vinfo+caller+str, args...)
			}
		}
	}
}

func (s *TaggedLogger) Debugf(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		if s.tag&tagBlockFilter == 0 {
			caller := s.CodeCallString()
			s.logger.Debug().Msgf(s.prefix+caller+str, args...)
		}
	}
}

func (s *TaggedLogger) Infof(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		if s.tag&tagBlockFilter == 0 {
			caller := s.CodeCallString()
			s.logger.Info().Msgf(s.prefix+caller+str, args...)
		}
	}
}

// Securityf logs a security-relevant event at Info level with a "SECURITY: " prefix.
func (s *TaggedLogger) Securityf(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		if s.tag&tagBlockFilter == 0 {
			caller := s.CodeCallString()
			s.logger.Info().Msgf("SECURITY: "+s.prefix+caller+str, args...)
		}
	}
}

func (s *TaggedLogger) Warnf(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		if s.tag&tagBlockFilter == 0 {
			caller := s.CodeCallString()
			s.logger.Warn().Msgf(s.prefix+caller+str, args...)
		}
	}
}

func (s *TaggedLogger) Errorf(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		if s.tag&tagBlockFilter == 0 {
			caller := s.CodeCallString()
			s.logger.Error().Msgf(s.prefix+caller+str, args...)
		}
	}
}

// Fatal does NOT call os.Exit(1)
// it is on the caller to shutdown if needed
func (s *TaggedLogger) Fatalf(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Error().Msgf("FATAL! "+s.prefix+caller+str, args...)
	}
}

func (s *TaggedLogger) Panicf(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Panic().Msgf(s.prefix+caller+str, args...)
	}
}

func (s *TaggedLogger) Tracef(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Trace().Msgf(s.prefix+caller+str, args...)
	}
}

func (s *TaggedLogger) Printf(str string, args ...interface{}) {
	if tagFilter > 0 && s.tag&tagFilter > 0 || tagFilter == 0 {
		if s.tag&tagBlockFilter == 0 {
			caller := s.CodeCallString()
			s.logger.Info().Msgf(s.prefix+caller+str, args...)
		}
	}
}

type Sublogger struct {
	prefix      string
	logger      zerolog.Logger
	codeContext bool
}

func GetSubLoggerAsLogr(name string) logr.Logger {
	return logr.New(&Sublogger{prefix: name + ": ", logger: log}).V(globalLogLevelLogr)
}

func GetSubLogger(name string) *Sublogger {
	return &Sublogger{prefix: name + ": ", logger: log}
}

// for logr.LogSink
func (s *Sublogger) Init(info logr.RuntimeInfo) {
	// noop
}

// for logr.LogSink
func (s *Sublogger) WithValues(keysAndValues ...any) logr.LogSink {
	s2 := fmt.Sprint(keysAndValues...)
	return &Sublogger{prefix: s.prefix + " [" + s2 + "]", logger: log}
}

// for logr.LogSink
func (s *Sublogger) WithName(name string) logr.LogSink {
	return &Sublogger{prefix: s.prefix + " " + name, logger: log}
}

// for logr.LogSink
func (s *Sublogger) Enabled(level int) bool {
	return level <= globalLogLevelLogr
}

// for logr.LogSink
func (s *Sublogger) Error(err error, msg string, keysAndValues ...any) {
	s2 := fmt.Sprint(keysAndValues...)
	s.Errorf("%v: %s", err, s2)
}

// for logr.LogSink
func (s *Sublogger) Info(level int, msg string, keysAndValues ...any) {
	s2 := fmt.Sprint(keysAndValues...)
	s.Infof("%v: %s", msg, s2)
}

func (s *Sublogger) SetCodeContext(codeContext bool) {
	s.codeContext = codeContext
}

func CodeCallString(depth int) string {
	_, fpath, line, ok := runtime.Caller(depth)
	if !ok {
		return "Unable to retrieve caller information"
	}
	dir, file := filepath.Split(fpath)
	dir = strings.TrimRight(dir, "/")
	dirName := filepath.Base(dir)
	return fmt.Sprintf(" (%s/%s:%d) ", dirName, file, line)
}

// get the caller's code context
func (s *Sublogger) CodeCallString() string {
	if s.codeContext {
		_, fpath, line, ok := runtime.Caller(2)
		if !ok {
			return "Unable to retrieve caller information"
		}
		dir, file := filepath.Split(fpath)
		dir = strings.TrimRight(dir, "/")
		dirName := filepath.Base(dir)
		return fmt.Sprintf(" (%s/%s:%d) ", dirName, file, line)
	}
	return ""
}

// Verbose Debugf
func (s *Sublogger) VDebugf(verbosity int, str string, args ...interface{}) {
	if debugVerbosity >= verbosity {
		var vinfo string
		if debugVerbosity >= 0 {
			vinfo = fmt.Sprintf(" [v%d] ", verbosity)
		}
		if tagFilter == 0 {
			caller := s.CodeCallString()
			s.logger.Debug().Msgf(s.prefix+vinfo+caller+str, args...)
		}
	}
}

func (s *Sublogger) Debugf(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Debug().Msgf(s.prefix+caller+str, args...)
	}
}

func (s *Sublogger) Infof(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Info().Msgf(s.prefix+caller+str, args...)
	}
}

// Securityf logs a security-relevant event at Info level with a "SECURITY: " prefix.
func (s *Sublogger) Securityf(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Info().Msgf("SECURITY: "+s.prefix+caller+str, args...)
	}
}

func (s *Sublogger) Warnf(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Warn().Msgf(s.prefix+caller+str, args...)
	}
}

func (s *Sublogger) Errorf(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Error().Msgf(s.prefix+caller+str, args...)
	}
}

// Fatal does NOT call os.Exit(1)
// it is on the caller to shutdown if needed
func (s *Sublogger) Fatalf(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Error().Msgf("FATAL! "+s.prefix+caller+str, args...)
	}
}

func (s *Sublogger) Panicf(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Panic().Msgf(s.prefix+caller+str, args...)
	}
}

func (s *Sublogger) Tracef(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Trace().Msgf(s.prefix+caller+str, args...)
	}
}

func (s *Sublogger) Printf(str string, args ...interface{}) {
	if tagFilter == 0 {
		caller := s.CodeCallString()
		s.logger.Info().Msgf(s.prefix+caller+str, args...)
	}
}

// --- Global (non-tagged) functions ---

func VDebugf(verbosity int, str string, args ...interface{}) {
	if debugVerbosity >= verbosity {
		var vinfo string
		if debugVerbosity >= 0 {
			vinfo = fmt.Sprintf("[v%d] ", verbosity)
		}
		if tagFilter == 0 {
			log.Debug().Msgf(vinfo+str, args...)
		}
	}
}

func Debugf(str string, args ...interface{}) {
	if tagFilter == 0 {
		log.Debug().Msgf(str, args...)
	}
}

func Printf(str string, args ...interface{}) {
	if tagFilter == 0 {
		log.Info().Msgf(str, args...)
	}
}

func Infof(str string, args ...interface{}) {
	if tagFilter == 0 {
		log.Info().Msgf(str, args...)
	}
}

// Securityf logs a security-relevant event at Info level with a "SECURITY: " prefix.
// Use for scanner probes, unknown hosts, blocked requests, and similar events that
// are not errors but are operationally significant.
func Securityf(str string, args ...interface{}) {
	if tagFilter == 0 {
		log.Info().Msgf("SECURITY: "+str, args...)
	}
}

func Warnf(str string, args ...interface{}) {
	if tagFilter == 0 {
		log.Warn().Msgf(str, args...)
	}
}

func Errorf(str string, args ...interface{}) {
	if tagFilter == 0 {
		log.Error().Msgf(str, args...)
	}
}

// Fatal does NOT call os.Exit(1)
// it is on the caller to shutdown if needed
func Fatalf(str string, args ...interface{}) {
	log.Error().Msgf("FATAL! "+str, args...)
}

func Panicf(str string, args ...interface{}) {
	log.Panic().Msgf(str, args...)
}

func Tracef(str string, args ...interface{}) {
	log.Trace().Msgf(str, args...)
}

type FLOptsT struct {
	runtimeLevel int
	pathsToShow  int
}

type FLOpts func(*FLOptsT)

// This adjusts the stack level returned by Loc() to get the caller of the log function
func StackLevel(level int) FLOpts {
	return func(o *FLOptsT) {
		o.runtimeLevel = level
	}
}

// This limits the file path to the last 'n' folders when printing the source file using Loc()
func Folders(n int) FLOpts {
	return func(o *FLOptsT) {
		o.pathsToShow = n
	}
}

// returns File and Line number as a string of format "file:line"
func Loc(opts ...FLOpts) string {
	allopts := FLOptsT{
		runtimeLevel: 1,
		pathsToShow:  3,
	}
	for _, opt := range opts {
		opt(&allopts)
	}

	_, file, line, ok := runtime.Caller(allopts.runtimeLevel)
	if !ok {
		file = "unknown"
		line = 0
	}

	file = trimFilePath(file, allopts.pathsToShow)

	return fmt.Sprintf("%s:%d", file, line)
}

// Helper function to trim file path to the last 'n' folders
func trimFilePath(filePath string, maxFolders int) string {
	parts := strings.Split(filepath.ToSlash(filePath), "/")
	if len(parts) > maxFolders {
		parts = parts[len(parts)-maxFolders:]
	}
	return filepath.Join(parts...)
}

// Helper function to wrap and log errors
func WrapAndLogError(format string, err error) error {
	wrappedErr := fmt.Errorf(format, err)
	log.Error().Err(wrappedErr).Msg("")
	return wrappedErr
}

type LogLevel int

// TagInfo contains information about a log tag
type TagInfo struct {
	Name        string
	Description string
	Tag         uint64
}

// GetAllTags returns all registered log tags
func GetAllTags() []TagInfo {
	tags := make([]TagInfo, 0, len(tagLoggers))
	for _, logger := range tagLoggers {
		tags = append(tags, TagInfo{
			Name:        logger.Name,
			Description: logger.Description,
			Tag:         logger.tag,
		})
	}
	return tags
}
