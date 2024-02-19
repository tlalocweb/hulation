package main

import (
	"fmt"
	"os"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/server"
	"github.com/tlalocweb/hulation/utils"

	"github.com/joho/godotenv"
)

func main() {
	app.ParseFlags()
	fmt.Printf("Starting Hulation\n")
	// should print if debug is enabled
	log.Debugf("Debug enabled")
	log.Tracef("Trace enabled")
	// Load .env file
	if err := godotenv.Load(".env"); err != nil {
		//		panic("Error loading .env file")
		log.Infof("Did not load .env file: (%s)", err.Error())
	}

	err := app.LoadConfig()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %s\n", err.Error())
		log.Fatalf("Error reading config: %s", err.Error())
		utils.CleanShutdown(1)
	}

	// if conf.Store == nil {
	// 	log.Fatalf("Store config is missing")
	// 	os.Exit(1)
	// }

	// setup DB connection
	if debuglevel := app.GetAppDebugLevel(); debuglevel > 0 {
		if !app.GetAppRuntimeOpts().NoDebugDbLog {
			model.SetDebugDBLogging(debuglevel)
		}
	}
	_, _, _, err = model.SetupAppDB(app.GetConfig())
	if err != nil {
		log.Fatalf("Error setting up database: %s", err.Error())
		utils.CleanShutdown(1)
	}

	utils.CleanShutdown(server.Run(app.GetConfig()))

}
