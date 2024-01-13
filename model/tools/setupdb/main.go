package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	// "gorm.io/driver/clickhouse"

	// _ "github.com/mailru/go-clickhouse"
	chapi "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/model"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
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
	if err := conn.PingContext(ctx); err != nil {
		if exception, ok := err.(*chapi.Exception); ok {
			//fmt.Printf("Catch exception [%d] %s \n%s\n", exception.Code, exception.Message, exception.StackTrace)
			err = fmt.Errorf("Catch exception [%d] %s \n%s\n", exception.Code, exception.Message, exception.StackTrace)
		} else {
			fmt.Printf("Error: %s\n", err.Error())
		}
	} else {
		fmt.Println("Ping OK")
	}
	return
}

func main() {

	var confpath, command string

	if len(os.Args) > 1 {
		confpath = os.Args[1]
	}
	if len(os.Args) > 2 {
		command = os.Args[2]
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

	switch command {
	case "deletedb":
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
	default:
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

		fmt.Printf("Automigrate done.\n")
	}

}
