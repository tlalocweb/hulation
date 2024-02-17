package middleware

// import (
// 	"github.com/gofiber/fiber/v2"
// 	"github.com/tlalocweb/hulation/model"
// 	"github.com/tlalocweb/hulation/utils"
// )

// func GetVisitor(c *fiber.Ctx) (cookie string, known model.Visitor, err error) {
// 	// Get cookie from request
// 	cookie = c.Cookies("visitor")

// 	if len(cookie) < 1 {
// 		cookie, err = utils.GenerateBase64RandomString(32)
// 	} else {
// 		// look for visitor in database
// 		known, err = model.GetVisitorByCookie(cookie)
// 	}
// 	return
// }
