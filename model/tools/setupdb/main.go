package main

import (
	"context"
	"fmt"
	"os"
	"time"

	// "gorm.io/driver/clickhouse"

	// _ "github.com/mailru/go-clickhouse"
	chapi "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/tlalocweb/hulation/config"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

func main() {

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

}
