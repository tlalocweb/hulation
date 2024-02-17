package handler

// utility functions for the handler package

import (
	"fmt"

	"github.com/gofiber/fiber/v3"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
)

func GetHostConfig(c fiber.Ctx) (hostconf *config.Server, host string, httperror int, err error) {
	host = c.Get("Host")
	hostonly := utils.GetHostOnly(host)
	hostconf = app.GetConfig().GetServer(hostonly)
	if hostconf != nil {
		if hostconf.IgnorePortInHeader {
			host = hostonly
		}
	} else {
		log.Errorf("Unknown host: %s", host)
		httperror = 404
		err = fmt.Errorf("unknown host: %s", host)
	}
	return
}
