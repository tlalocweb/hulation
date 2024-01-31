package model

import (
	"context"
	"time"

	"github.com/tlalocweb/hulation/log"
	"gorm.io/gorm/logger"
)

type DBLogger interface {
	LogMode(logger.LogLevel) logger.Interface
	Info(context.Context, string, ...interface{})
	Warn(context.Context, string, ...interface{})
	Error(context.Context, string, ...interface{})
	Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error)
}

type DBLoggerImpl struct {
	level logger.LogLevel
}

func (l *DBLoggerImpl) LogMode(level logger.LogLevel) logger.Interface {
	l.level = level
	return l
}

func (l *DBLoggerImpl) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.level >= logger.Info {
		log.Infof("model: "+msg, data...)
	}
}

func (l *DBLoggerImpl) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.level >= logger.Warn {
		log.Warnf("model: "+msg, data...)
	}
}

func (l *DBLoggerImpl) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.level >= logger.Error {
		log.Errorf("model: "+msg, data...)
	}
}

func (l *DBLoggerImpl) Trace(tx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	// logger.Trace().Msgf("trace: sql:%s rows: %d\n", sql, rowsAffected)
}

func GetNewDBLogger(level logger.LogLevel) DBLogger {
	return &DBLoggerImpl{
		level: level,
	}
}
