package main

import (
	"context"
	"fmt"
	"os"
	"time"

	// "gorm.io/driver/clickhouse"

	// _ "github.com/mailru/go-clickhouse"
	// click "github.com/mailru/go-clickhouse/v2"
	chapi "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/tlalocweb/hulation/config"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

// func Connect(connStr string) (conn *sql.DB, err error) {
// 	conn, err = sql.Open("clickhouse", connStr)
// 	if err != nil {
// 		return
// 	}
// 	err = conn.Ping()
// 	if err != nil {
// 		return
// 	}
// 	return
// }

func main() {

	// dsn := "clickhouse://default:@localhost:8123/gorm?dial_timeout=10s&read_timeout=20s"
	// _, err := gorm.Open(clickhouse.Open(dsn), &gorm.Config{})

	// connStr := "http://127.0.0.1:8123/db?user=default"
	// //	connStr := "http://127.0.0.1:8123/db?user=default&password=default"
	// sqlConn, err := Connect(connStr)
	// if err != nil {
	// 	fmt.Printf("failed to connect database: %s", err.Error())
	// 	panic("failed to connect database")
	// }

	// opts := &click.Options{
	// 	Addr: []string{"127.0.0.1:8123"},
	// 	Auth: click.Auth{
	// 		Database: "default",
	// 		Username: "default",
	// 		Password: "",
	// 	},
	// 	TLS: nil,
	// 	// &tls.Config{
	// 	// 	InsecureSkipVerify: true,
	// 	// },
	// 	Settings: click.Settings{
	// 		"max_execution_time": 60,
	// 	},
	// 	DialTimeout: 5 * time.Second,
	// 	// Compression: &click.Compression{
	// 	// 	Method: click.CompressionLZ4,
	// 	// 	Level:  1,
	// 	// },
	// 	Debug: true,
	// }

	// sqlDB := click.OpenDB(opts)

	// refer to https://github.com/ClickHouse/clickhouse-go

	// write code to get config file path from first argument of command line

	var confpath string

	if len(os.Args) > 1 {
		confpath = os.Args[1]
	}
	if len(confpath) < 1 {
		fmt.Printf("Error: config file path not provided.\n")
		os.Exit(1)
	}
	hulationconf, err := config.LoadConfig(confpath)

	if err != nil {
		fmt.Printf("Error loading config: (%s) %s", confpath, err.Error())
		os.Exit(1)
	}

	dsn := config.GetDSNFromConfig(hulationconf)
	fmt.Printf("Connecting to %s\n", dsn)
	//	var dsn = "clickhouse://default:@127.0.0.1:9000/db?dial_timeout=200ms&max_execution_time=60"

	fmt.Printf("testing clickhouse-go library...\n")

	conn := chapi.OpenDB(&chapi.Options{
		Addr: []string{fmt.Sprintf("%s:%d", hulationconf.DBConfig.Host, hulationconf.DBConfig.Port)},
		Auth: chapi.Auth{
			Database: hulationconf.DBConfig.DBName,
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
	ctx := chapi.Context(context.Background(), chapi.WithSettings(chapi.Settings{
		"max_block_size": 10,
	}), chapi.WithProgress(func(p *chapi.Progress) {
		fmt.Println("progress: ", p)
	}))
	if err := conn.PingContext(ctx); err != nil {
		if exception, ok := err.(*chapi.Exception); ok {
			fmt.Printf("Catch exception [%d] %s \n%s\n", exception.Code, exception.Message, exception.StackTrace)
		} else {
			fmt.Printf("Error: %s\n", err.Error())
		}
		os.Exit(1)
	} else {
		fmt.Println("Ping OK")
	}
	//	conn.Close()
	fmt.Printf("testing gorm w/ clickhouse driver...\n")

	if _, err = gorm.Open(clickhouse.New(clickhouse.Config{
		Conn: conn,
	}), &gorm.Config{}); err != nil {
		fmt.Printf("failed to connect database, got error %v", err)
		os.Exit(1)
	}

	fmt.Printf("No error. Connectivity ok.\n")

	// _, err := gorm.Open(clickhouse.New(click.Config{
	// 	DSN:                          dsn,
	// 	Conn:                         conn,     // initialize with existing database conn
	// 	DisableDatetimePrecision:     true,     // disable datetime64 precision, not supported before clickhouse 20.4
	// 	DontSupportRenameColumn:      true,     // rename column not supported before clickhouse 20.4
	// 	DontSupportEmptyDefaultValue: false,    // do not consider empty strings as valid default values
	// 	SkipInitializeWithVersion:    false,    // smart configure based on used version
	// 	DefaultGranularity:           3,        // 1 granule = 8192 rows
	// 	DefaultCompression:           "LZ4",    // default compression algorithm. LZ4 is lossless
	// 	DefaultIndexType:             "minmax", // index stores extremes of the expression
	// 	DefaultTableEngineOpts:       "ENGINE=MergeTree() ORDER BY tuple()",
	// }), &gorm.Config{})

	// // if sqlDB == nil {
	// // 	fmt.Printf("failed to connect database.")
	// // 	panic("failed to connect database")
	// // }

	// // _, err = gorm.Open(sqlConn, &gorm.Config{})

	// if err != nil {
	// 	fmt.Printf("failed to start gorm driver: %s", err.Error())
	// 	panic("failed to start gorm clickhouse driver")
	// }

	// // Auto Migrate
	// db.AutoMigrate(&User{})
	// // Set table options
	// db.Set("gorm:table_options", "ENGINE=Distributed(cluster, default, hits)").AutoMigrate(&User{})

	// // Set table cluster options
	// db.Set("gorm:table_cluster_options", "on cluster default").AutoMigrate(&User{})

	// // Insert
	// db.Create(&User{Name: "Angeliz", Age: 18})

	// // Select
	// db.Find(&User{}, "name = ?", "Angeliz")

	// // Batch Insert
	// user1 := User{Age: 12, Name: "Bruce Lee"}
	// user2 := User{Age: 13, Name: "Feynman"}
	// user3 := User{Age: 14, Name: "Angeliz"}
	// var users = []User{user1, user2, user3}
	// db.Create(&users)
}
