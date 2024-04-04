package model

import (
	"fmt"
	"net/url"
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
	// if true, then when serving the redirect target, we will ignore the port in the redirect url
	// so for instance if the redirect target is https://localhost:1313/mypage and our server is called localhost then
	// we will serve the target as http://localhost/mypage as either redirect or static serve (if the file exists)
	IgnorePort bool `json:"ignore_port"`
	// By default if we are serving the target of the redirect,
	// then we will attempt to just serve the static page
	// directly instead of redirecting to it.
	// if NoServe is true then we will _not_ do this.
	NoServe bool `json:"no_serve"`
}

type LanderInstance struct {
	lander     *Lander
	fullUrl    string
	redirect   string
	fsRoot     string // the root of the filesystem for the static path
	staticPath string // the relative path in this filesystem to the static file
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
	landerInstances.SetAlways(l.UrlPostfix, i)
	return
}

func (l *Lander) Update(db *gorm.DB) (i *LanderInstance, err error) {
	// err = l.Validate(requesturlpostfix, db)
	// if err != nil {
	// 	err = fmt.Errorf("validation error: %v", err)
	// 	return
	// }
	err = db.Connection(func(tx *gorm.DB) (err error) {
		err = tx.Exec("ALTER TABLE landers UPDATE name = ?, description = ?, server = ?, url_postfix = ?, redirect = ? WHERE id = ?", l.Name, l.Description, l.Server, l.UrlPostfix, l.Redirect, l.ID).Error
		return
	})
	if err != nil {
		return
	}
	i, err = createLanderInstance(l)
	if err != nil {
		err = fmt.Errorf("error creating lander instance: %v", err)
		return
	}
	landerInstances.SetAlways(l.UrlPostfix, i)
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

	hostconf := app.GetConfig().GetServer(l.Server)
	if hostconf == nil {
		err = fmt.Errorf("server %s unknown", l.Server)
		return
	}
	i.fullUrl = fmt.Sprintf("%s%s", hostconf.GetExternalUrl(), path.Join(app.GetConfig().VisitorPrefix, app.GetConfig().LanderPath, i.lander.UrlPostfix))
	log.Debugf("creating lander instance for %s: %s", l.Name, i.fullUrl)
	uredirect, err := url.Parse(l.Redirect)
	// ulander, err2 := url.Parse(i.fullUrl)
	var fp string
	if err == nil { //|| err2 == nil {
		// look through all servers and see if we are serving a static path which can match this
		for _, s := range app.GetConfig().Servers {
			if uredirect.Path == "/" {
				uredirect.Path = "/" + s.RootIndex
			}
			log.Debugf("checking server %s: %s vs %s for path '%s'", s.Host, s.GetExternalUrl(), uredirect.Host, uredirect.Path)
			check := s.GetExternalHostPort() == uredirect.Host
			if l.IgnorePort {
				check = s.Host == utils.GetJustHost(uredirect.Host)
			}
			if check {
				log.Debugf("Lander %s redirect matches server %s - path %s", l.Name, s.Host, uredirect.Path)
				if len(s.Root) > 0 {
					// check if a file exists
					fp = path.Join(s.Root, path.Base(uredirect.Path))
					if utils.FileExists(fp) {
						i.staticPath = uredirect.Path
						i.fsRoot = s.Root
						break
					}
				}
				for _, f := range s.NonRootStaticFolders {
					if len(f.Root) > 0 {
						fp = path.Join(f.Root, path.Base(uredirect.Path))
						if utils.FileExists(fp) {
							i.staticPath = uredirect.Path
							i.fsRoot = f.Root
							break
						}
					}
				}
				log.Errorf("Lander %s redirect matches server %s but file %s does not exist", l.Name, s.Host, fp)
			}
		}
	} else {
		log.Errorf("error parsing redirect or full url: %v", err)
	}
	if len(i.staticPath) < 1 {
		log.Debugf("Lander %s redirect does not match any static path- using 302", l.Name)
		i.redirect = l.Redirect
	}
	//	hostconf := app.GetConfig().GetServer(l.Server)
	return
}

func (i *LanderInstance) GetFinalUrl() string {
	return i.fullUrl
}

func (i *LanderInstance) GetLander() *Lander {
	return i.lander
}

func (i *LanderInstance) GetRedirect() string {
	return i.redirect
}

func (i *LanderInstance) GetStaticPath() string {
	return i.staticPath
}

func (i *LanderInstance) GetFsRoot() string {
	return i.fsRoot
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

func GetLanderByName(db *gorm.DB, id string) (l *Lander, i *LanderInstance, err error) {
	l = &Lander{}
	err = db.Model(l).Where("name = ?", id).First(l).Error
	if err != nil {
		// landerInstances.Del(id)
		if err == gorm.ErrRecordNotFound {
			log.Debugf("no lander by name %s", id)
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

func getPredifinedNameFromName(name string, host string) string {
	return fmt.Sprintf("predefined-config:%s-%s", host, name)
}

// PrelaodDefinedLanders will preload the landers defined in the hula config file
// into the database and the http handlers
// It only throws an error if it can't create a lander at all - not if it has
// an issue with the db
func PreloadDefinedLanders(db *gorm.DB) (err error) {
	//lander *config.DefinedLander, server *config.Server
	for _, server := range app.GetConfig().Servers {
		for _, lander := range server.Landers {
			defname := getPredifinedNameFromName(lander.Name, server.Host)
			var l *Lander
			var i *LanderInstance
			l, i, err = GetLanderByName(db, defname)
			if err != nil {
				log.Errorf("PreloadDefinedLanders: error getting lander by name: %s", err.Error())
			}
			if l != nil && i != nil {
				// update (may or may not have changed)
				l.Name = defname
				l.Server = server.Host
				l.UrlPostfix = lander.UrlId
				l.NoServe = lander.NoServe
				l.IgnorePort = !lander.NoticePort
				// is the lander.Redirect config setting a full URL or just a path?
				u, err2 := url.Parse(lander.Redirect)
				if err2 != nil {
					log.Errorf("PreloadDefinedLanders: error parsing redirect url: %s - Using the setting anyway.", err.Error())
					l.Redirect = lander.Redirect
				} else {
					if u.Host == "" {
						l.Redirect = fmt.Sprintf("%s%s", server.GetExternalUrl(), lander.Redirect)
					} else {
						if lander.Redirect[0] == '/' {
							l.Redirect = lander.Redirect
						} else {
							err = fmt.Errorf("PreloadDefinedLanders: lander %s (%s) for server %s must be a full URL or an absolute path", lander.Name, defname, server.Host)
							return
						}
					}
				}
				log.Debugf("PreloadDefinedLanders: updating lander %s for host %s", l.Name, server.Host)
				i, err = l.Update(db)
				if err != nil {
					log.Errorf("PreloadDefinedLanders: error updating lander: %s", err.Error())
					err = nil
					return
				}
				return
			}

			l = NewLander()
			l.Name = defname
			l.Server = server.Host
			l.UrlPostfix = lander.UrlId
			l.Redirect = fmt.Sprintf("%s%s", server.GetExternalUrl(), lander.Redirect)
			l.NoServe = lander.NoServe
			// l := &Lander{
			// 	Name:       lander.Name,
			// 	Server:     server.Host,
			// 	UrlPostfix: lander.UrlPostfix,
			// 	Redirect:   lander.Redirect,
			// }
			log.Debugf("PreloadDefinedLanders: creating lander %s (%s) for host %s", l.Name, defname, server.Host)
			_, err = l.Commit(lander.UrlId, db)

			if err != nil {
				log.Errorf("PreloadDefinedLanders: error committing lander instance: %s", err.Error())
				return
			}
		}
	}
	return
}

func GetLanderByUrlPostfix(db *gorm.DB, urlpost string) (l *Lander, i *LanderInstance, err error) {
	l = &Lander{}
	if v, ok := landerInstances.Get(urlpost); ok {
		i, ok = v.(*LanderInstance)
		if ok {
			l = i.lander
			return
		}
	}
	err = db.Model(l).Where("url_postfix = ?", urlpost).First(l).Error
	if err != nil {
		landerInstances.Del(urlpost)
		if err == gorm.ErrRecordNotFound {
			log.Debugf("no lander by url_postfix %s", urlpost)
			err = nil
			return
		}
	} else {
		i, err = createLanderInstance(l)
		if err != nil {
			log.Debugf("error creating lander instance: %s", err.Error())
			return
		}
		landerInstances.SetAlways(urlpost, i)
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
