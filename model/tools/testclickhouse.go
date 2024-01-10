package main

import (
	"fmt"
	"time"

	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"

	click "github.com/ClickHouse/clickhouse-go/v2"
)

func main() {

	// dsn := "clickhouse://default:@localhost:8123/gorm?dial_timeout=10s&read_timeout=20s"
	// _, err := gorm.Open(clickhouse.Open(dsn), &gorm.Config{})

	opts := &click.Options{
		Addr: []string{"127.0.0.1:8123"},
		Auth: click.Auth{
			Database: "default",
			Username: "default",
			Password: "",
		},
		TLS: nil,
		// &tls.Config{
		// 	InsecureSkipVerify: true,
		// },
		Settings: click.Settings{
			"max_execution_time": 60,
		},
		DialTimeout: 5 * time.Second,
		// Compression: &click.Compression{
		// 	Method: click.CompressionLZ4,
		// 	Level:  1,
		// },
		Debug: true,
	}

	sqlDB := click.OpenDB(opts)

	if sqlDB == nil {
		fmt.Printf("failed to connect database.")
		panic("failed to connect database")
	}

	_, err := gorm.Open(clickhouse.New(clickhouse.Config{
		Conn: sqlDB, // initialize with existing database conn
	}))

	if err != nil {
		fmt.Printf("failed to start gorm driver: %s", err.Error())
		panic("failed to start gorm clickhouse driver")
	}

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
