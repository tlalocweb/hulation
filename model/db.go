package model

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	stdlog "log"

	chapi "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This is the same as gorm.Model expect does not have
// the DeletedAt field. This is because the Clickhouse driver does not
// support the DeletedAt type.
// See: https://github.com/go-gorm/clickhouse/issues/12
type HModel struct {
	ID        string `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	// DeletedAt DeletedAt `gorm:"index"`
}

type HasUID interface {
	GenID() string
}

func setupInitConn(hulationconf *config.Config, dbname string) (conn *sql.DB, ctx context.Context, err error) {
	// dsn := config.GetDSNFromConfig(hulationconf)
	// fmt.Printf("Connecting to %s\n", dsn)
	//	var dsn = "clickhouse://default:@127.0.0.1:9000/db?dial_timeout=200ms&max_execution_time=60"

	log.Debugf("connecting with clickhouse-go library...\n")

	opts := &chapi.Options{
		Addr: []string{fmt.Sprintf("%s:%d", hulationconf.DBConfig.Host, hulationconf.DBConfig.Port)},
		Auth: chapi.Auth{
			Database: dbname,
			Username: hulationconf.DBConfig.Username,
			Password: hulationconf.DBConfig.Password,
		},
		Settings: chapi.Settings{
			"max_execution_time":     60,
			"date_time_input_format": "best_effort",
		},
		DialTimeout: 5 * time.Second,
		Compression: &chapi.Compression{
			Method: chapi.CompressionLZ4,
		},
	}

	if lowLevelDebug {
		opts.Debug = true
		opts.Debugf = func(format string, v ...interface{}) {
			fmt.Printf("clickhouse-go: "+format+"\n", v...)
		}
	}

	conn = chapi.OpenDB(opts)

	conn.SetMaxIdleConns(5)
	conn.SetMaxOpenConns(10)
	conn.SetConnMaxLifetime(time.Hour)
	ctx = chapi.Context(context.Background(), chapi.WithSettings(chapi.Settings{
		"max_block_size": 10,
	}), chapi.WithProgress(func(p *chapi.Progress) {
		log.Debugf("progress: %+v\n", p)
	}))
	if err = conn.PingContext(ctx); err != nil {
		if exception, ok := err.(*chapi.Exception); ok {
			//fmt.Printf("Catch exception [%d] %s \n%s\n", exception.Code, exception.Message, exception.StackTrace)
			err = fmt.Errorf("catch exception [%d] %s --> %s", exception.Code, exception.Message, exception.StackTrace)
		} else {
			log.Errorf("db error: %s\n", err.Error())
		}
	} else {
		log.Infof("DB Ping OK")
	}
	return
}

var _db *gorm.DB
var _sqlDB *sql.DB

// get's the gorm DB object
func GetDB() *gorm.DB {
	return _db
}

// get's the sql.DB object - for low level SQL queries
func GetSQLDB() *sql.DB {
	return _sqlDB
}

type PreConnectModelFunc func(ctx context.Context, conn *sql.DB) error

var useGormLogger = false
var lowLevelDebug bool
var defaultLogLevel = logger.Silent

func SetDebugDBLogging(debuglevel int) {
	useGormLogger = true
	defaultLogLevel = logger.Info
	if debuglevel > 1 {
		lowLevelDebug = true
	}
}

func SetLogLevel(level logger.LogLevel) {
	defaultLogLevel = level
}

// SetupDB sets up a connection to the database and automigrates models
// based on the configuration. premodelhook allows for a function to be
// called before the models are automigrated and before the database is
// connected. Usually this will create the DB if it does not exist. Can be nil.
func SetupDB(hulationconf *config.Config, premodelhook PreConnectModelFunc) (conn *sql.DB, gormdb *gorm.DB, ctx context.Context, err error) {
	if premodelhook != nil {
		log.Debugf("Connecting to clickhouse (premodelhook)\n")
		conn, ctx, err = setupInitConn(hulationconf, "")
		if err != nil {
			err = fmt.Errorf("error on connect: %s", err.Error())
			return
		}
		err = premodelhook(ctx, conn)
		if err != nil {
			log.Errorf("Error on premodelhook: %s", err.Error())
			return
		}
		conn.Close()
	}
	log.Debugf("Connecting to %s\n", hulationconf.DBConfig.DBName)
	conn, ctx, err = setupInitConn(hulationconf, hulationconf.DBConfig.DBName)
	if err != nil {
		err = fmt.Errorf("error on connect: %s", err.Error())
		return
	}

	gormconf := &gorm.Config{}

	if useGormLogger {
		newLogger := logger.New(
			stdlog.New(os.Stdout, "\r\n", stdlog.LstdFlags), // io writer
			logger.Config{
				SlowThreshold:             time.Second,     // Slow SQL threshold
				LogLevel:                  defaultLogLevel, // Log level
				IgnoreRecordNotFoundError: true,            // Ignore ErrRecordNotFound error for logger
				ParameterizedQueries:      true,            // Don't include params in the SQL log
				Colorful:                  true,            // Disable color
			},
		)
		gormconf.Logger = newLogger
	} else {
		gormconf.Logger = GetNewDBLogger(defaultLogLevel)
	}

	log.Infof("Connected to %s\n", hulationconf.DBConfig.DBName)
	if gormdb, err = gorm.Open(clickhouse.New(clickhouse.Config{
		Conn: conn,
	}), gormconf); err != nil {
		log.Errorf("failed to connect database model, got error (gorm) %v\n", err)
		err = fmt.Errorf("failed to connect database, got error %v", err)
		return
	} else {
		log.Infof("Model connected\n")
	}
	log.Debugf("No error. Connectivity ok.\n")
	log.Infof("DB: Automigrate models...\n")
	err = AutoMigrateVisitorModels(gormdb)
	if err != nil {
		log.Errorf("Error automigrating visitor models: %s", err.Error())
	}
	err = AutoMigrateLandingModels(gormdb)
	if err != nil {
		log.Errorf("Error automigrating landing models: %s", err.Error())
	}
	err = AutoMigrateFormModels(gormdb)
	if err != nil {
		log.Errorf("Error automigrating form models: %s", err.Error())
	}
	err = AutoMigrateAuthModels(gormdb)
	if err != nil {
		log.Errorf("Error automigrating auth models: %s", err.Error())
	}

	return
}

func SetupAppDB(hulationconf *config.Config) (conn *sql.DB, db *gorm.DB, ctx context.Context, err error) {
	if db != nil {
		err = fmt.Errorf("db already setup")
		return
	}
	conn, db, ctx, err = SetupDB(hulationconf, func(ctx context.Context, conn *sql.DB) (err error) {
		// create database if not exists
		_, err = conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", hulationconf.DBConfig.DBName))
		return
	})
	_db = db
	_sqlDB = conn
	return
}

// CloseDB closes the database connection
// Should be called on shutdown
func CloseDB(gormdb *gorm.DB) {
	if gormdb != nil {
		sqlDB, _ := gormdb.DB()
		sqlDB.Close()
	}
}

func ShutdownAppDB() {
	CloseDB(_db)
	_db = nil
}
