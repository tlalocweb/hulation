package model

import (
	"fmt"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
	"gorm.io/gorm"
)

type Lander struct {
	HModel
	Name        string `json:"name"`
	Description string `json:"description"`
	Hits        uint64 `json:"hits"`
	Server      string `json:"server"` // the server name - as in the server defined in the hula config file
	UrlPostfix  string `json:"url"`    // the postfix path that is provided on the url - usually just the lander id  - like /19181
	Redirect    string `json:"redirect"`
}

type LanderInstance struct {
	lander     *Lander
	fullUrl    string
	redirect   string
	staticPath string
}

const (
	sqlCreateLanderModel = `
	CREATE TABLE IF NOT EXISTS landers
	(
		^id^ String,
		^created_at^ DateTime64(3),
		^updated_at^ DateTime64(3),
		^name^ String,
		^hits^ UInt64,
		^description^ String,
		^server^ String,
		^url_postfix^ String,
		^redirect^ String
	)
	ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY id;`
)

func NewLander() *Lander {
	return &Lander{}
}

func (l *Lander) Validate(requesturlpostfix string, db *gorm.DB) (err error) {
	if len(l.Name) < 1 {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
	if len(l.Server) < 1 {
		return &ValidationError{Field: "server", Message: "server is required"}
	}
	hostconf := app.GetConfig().GetServer(l.Server)
	if hostconf == nil {
		return &ValidationError{Field: "server", Message: fmt.Sprintf("server %s unknown", l.Server)}
	}
	if len(requesturlpostfix) > 0 {
		// check the database to see if this UrlPostfix is already in use
		var count int64
		err = db.Model(l).Where("url_postfix = ?", requesturlpostfix).Count(&count).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return &ValidationError{Field: "url", Message: fmt.Sprintf("error checking for url_postfix: %v", err)}
			}
		}
		if count > 0 {
			var post string
			post, err = utils.GenerateBase64RandomStringNoPadding(8)
			if err != nil {
				return &ValidationError{Field: "url", Message: fmt.Sprintf("error generating random postfix: %v", err)}
			}
			l.UrlPostfix = requesturlpostfix + "-" + post
			log.Warnf("url_postfix %s already in use - using %s as alternate", requesturlpostfix, l.UrlPostfix)
		} else {
			l.UrlPostfix = requesturlpostfix
		}
	}
	if len(l.UrlPostfix) < 1 {
		var post string
		post, err = utils.GenerateBase64RandomStringNoPadding(8)
		if err != nil {
			return &ValidationError{Field: "url", Message: fmt.Sprintf("error generating random postfix: %v", err)}
		}
		l.UrlPostfix = post
	}
	if len(l.Redirect) < 1 {
		return &ValidationError{Field: "redirect", Message: "redirect is required"}
	}
	return nil
}

func (l *Lander) BeforeCreate(tx *gorm.DB) (err error) {
	if len(l.UrlPostfix) < 1 {
		err = l.Validate("", tx)
		if err != nil {
			return
		}
	}
	// UUID version 7
	if len(l.ID) < 1 {
		uuid7, err := uuid.NewV7()
		if err != nil {
			return err
		}
		l.ID = uuid7.String()
	}
	return
}

func AutoMigrateLanderModels(db *gorm.DB) (err error) {
	err = db.Exec(utils.SqlStr(sqlCreateLanderModel)).Error
	if err != nil {
		return err
	}

	err = db.AutoMigrate(&Lander{})
	if err != nil {
		return err
	}
	return nil
}

func (l *Lander) Commit(requesturlpostfix string, db *gorm.DB) (i *LanderInstance, err error) {
	err = l.Validate(requesturlpostfix, db)
	if err != nil {
		err = fmt.Errorf("validation error: %v", err)
		return
	}
	err = db.Create(l).Error
	if err != nil {
		return
	}
	i, err = createLanderInstance(l)
	if err != nil {
		err = fmt.Errorf("error creating lander instance: %v", err)
		return
	}
	// update cache
	landerInstances.SetAlways(l.ID, i)
	return
}

func (l *Lander) AddHit(db *gorm.DB) (err error) {
	l.Hits++
	err = db.Create(l).Error
	if err != nil {
		return err
	}
	return nil
}

// func (l *Lander) Update(requesturlpostfix string, db *gorm.DB) (err error) {
// 	err = l.Validate(requesturlpostfix, db)
// 	if err != nil {
// 		err = fmt.Errorf("validation error: %v", err)
// 		return
// 	}
// 	err = db.Save(l).Error
// 	if err != nil {
// 		return err
// 	}
// 	// update cache
// 	var i *LanderInstance
// 	i, err = createLanderInstance(l)
// 	if err != nil {
// 		err = fmt.Errorf("error creating lander instance: %v", err)
// 		return
// 	}
// 	landerInstances.SetAlways(l.ID, i)
// 	return nil
// }

// var landerCache *utils.InMemCache
var landerInstances *utils.InMemCache // landerid:*landerInstance

func init() {
	//	landerCache = utils.NewInMemCache().WithExpiration(72 * time.Hour).Start()
	landerInstances = utils.NewInMemCache().WithExpiration(72 * time.Hour).Start()
}

func createLanderInstance(l *Lander) (i *LanderInstance, err error) {
	i = &LanderInstance{
		lander: l,
	}
	// TODO: determine whether we do a redirect or serve a static file
	i.redirect = l.Redirect

	hostconf := app.GetConfig().GetServer(l.Server)
	if hostconf == nil {
		err = fmt.Errorf("server %s unknown", l.Server)
		return
	}
	i.fullUrl = fmt.Sprintf("%s%s", hostconf.GetExternalUrl(), path.Join(app.GetConfig().VisitorPrefix, app.GetConfig().LanderPath, i.lander.UrlPostfix))
	return
}

func (i *LanderInstance) GetFinalUrl() string {
	return i.fullUrl
}

func (i *LanderInstance) DoRedirect() (ok bool, redirect string) {
	if len(i.redirect) < 1 {
		return false, ""
	}
	return true, i.redirect
}

func (i *LanderInstance) DoStatic() (ok bool, staticPath string) {
	if len(i.staticPath) < 1 {
		return false, ""
	}
	return true, i.staticPath
}

func GetLanderById(db *gorm.DB, id string) (l *Lander, i *LanderInstance, err error) {
	l = &Lander{}
	// if v, ok := landerInstances.Get(id); ok {
	// 	i, ok = v.(*LanderInstance)
	// 	if ok {
	// 		l = i.lander
	// 		return
	// 	}
	// }
	err = db.Model(l).Where("id = ?", id).First(l).Error
	if err != nil {
		// landerInstances.Del(id)
		if err == gorm.ErrRecordNotFound {
			log.Debugf("no lander by id %s", id)
			err = nil
			return
		}
	} else {
		i, err = createLanderInstance(l)
		if err != nil {
			log.Debugf("error creating lander instance: %s", err.Error())
			return
		}
		// landerInstances.SetAlways(id, i)
	}

	return
}

func GetLanderByUrlPostfix(db *gorm.DB, id string) (l *Lander, i *LanderInstance, err error) {
	l = &Lander{}
	if v, ok := landerInstances.Get(id); ok {
		i, ok = v.(*LanderInstance)
		if ok {
			l = i.lander
			return
		}
	}
	err = db.Model(l).Where("url_postfix = ?", id).First(l).Error
	if err != nil {
		landerInstances.Del(id)
		if err == gorm.ErrRecordNotFound {
			log.Debugf("no lander by url_postfix %s", id)
			err = nil
			return
		}
	} else {
		i, err = createLanderInstance(l)
		if err != nil {
			log.Debugf("error creating lander instance: %s", err.Error())
			return
		}
		landerInstances.SetAlways(id, i)
	}

	return
}

func OptimizeLanderModels(db *gorm.DB) (err error) {
	err = db.Exec("OPTIMIZE TABLE landers FINAL").Error
	if err != nil {
		return err
	}
	return nil
}

func DeleteLander(db *gorm.DB, id string) (err error) {
	landerInstances.Del(id)
	err = db.Delete(&Lander{}, "id = ?", id).Error
	if err != nil {
		return err
	}
	return nil
}

// func (l *Lander) InstallRoute(fiberapp *fiber.App) (err error) {
// 	// if we are serving the redirect target then we will serve the target directly

// 	// if we should do a 302 redirect:
// 	fiberapp.Get(l.UrlPostfix, func(c *fiber.Ctx) error {
// 		l.Hits++
// 		// TODO - add cookie
// 		err := c.Redirect(l.Redirect)

// 		if err != nil {
// 			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
// 		}
// 		return nil
// 	})
// 	return nil
// }

// func (l *Lander) UninstallRoute() (err error) {
// 	return nil
// }
