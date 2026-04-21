package main

import (
	"context"
	"fmt"
	"os"
	"path"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/server"
	"github.com/tlalocweb/hulation/utils"

	"go.izuma.io/conftagz"
)

func main() {
	app.ParseFlags()

	fmt.Printf("Starting Hulation (%s) config: %s\n", config.Version, path.Clean(app.GetConfigPath()))
	fmt.Printf("  Build date: %s\n", config.BuildDate)
	// should print if debug is enabled
	log.Debugf("Debug enabled")
	log.Tracef("Trace enabled")
	// Load .env file (if present) and set vars in the process environment
	envVars, err := conftagz.LoadDotEnvFile(".env")
	if err != nil {
		log.Errorf("Error loading .env file: %s", err.Error())
	} else if len(envVars) > 0 {
		for k, v := range envVars {
			os.Setenv(k, v)
		}
		log.Infof("Loaded %d variable(s) from .env file", len(envVars))
	}

	err = app.LoadConfig()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %s\n", err.Error())
		log.Fatalf("Error reading config: %s", err.Error())
		utils.CleanShutdown(1)
	}
	app.ApplyLogTagConfig()

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

	utils.CleanShutdown(server.RunUnified(context.Background(), app.GetConfig()))

}
