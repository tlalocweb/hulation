package handler

func Status(ctx RequestCtx) error {
	return ctx.SendJSON(map[string]interface{}{"status": "success", "message": "ok.", "data": nil})
}

func StatusOKAuth(ctx RequestCtx) error {
	hostconf, _, httperr, err := GetHostConfig(ctx)
	if err != nil {
		return ctx.Status(httperr).SendString(err.Error())
	}
	id := ctx.Query("h")
	if id != hostconf.ID {
		return ctx.Status(400).SendString("host id mismmatch")
	}
	return ctx.Status(200).SendString("ok")
}
