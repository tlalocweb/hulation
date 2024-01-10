package main

import (
	"fmt"
	"hulation/config"
	"hulation/log"
	"hulation/router"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file
	if err := godotenv.Load(".env"); err != nil {
		panic("Error loading .env file")
	}

	conf, err := config.ReadConfig("config.yaml")

	if err != nil {
		log.Fatalf("Error reading config: %v", err)
		os.Exit(1)
	}

	if conf.Store == nil {
		log.Fatalf("Store config is missing")
		os.Exit(1)
	}

	app := fiber.New()
	app.Use(cors.New())

	//	store.ConnectDB()

	router.SetupRoutes(app)
	log.Fatalf(app.Listen(fmt.Sprintf(":%d", conf.Port)).Error())
}
