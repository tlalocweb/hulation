package model

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/utils"
	"gorm.io/gorm"
)

type IP struct {
	IP         string `gorm:"primaryKey" json:"ip"`
	FirstSeen  time.Time
	LastSeenAt time.Time
	Seen       int     // how many times this IP has been seen
	Events     []Event `gorm:"foreignKey:FromIP;references:IP"` // 1:many
}

const (
	sqlCreateIP = `
	CREATE TABLE IF NOT EXISTS ips
	(
		^ip^ String,
		^first_seen^ DateTime64(3),
		^last_seen_at^ DateTime64(3),
		^seen^ UInt64
	)
	ENGINE = SummingMergeTree()
	ORDER BY ip;`
)

// SummingMergeTree() is not the best solution. We need something which
// keeps the earliest first_seen and the latest last_seen

func (ip *IP) Scan(value interface{}) error {
	ip.IP = string(value.([]byte))
	return nil
}

func (ip IP) Value() (interface{}, error) {
	return ip.IP, nil
}

func SawIP(db *gorm.DB, ip string) (err error) {

	return
}

type AcceptedOnWeb struct {
	HModel
	ServiceName string // "google analytics" or "google tag manager" or "yandex metrika" or "facebook pixel" etc.
	Answer      bool   // true or false // true - accepted, false - not accepted
}

// keeps track of the last broswer side cookies
// this visitor used. Useful if Safari or other browser (or the user) decide to wipe out
// cookies on the browser side.
// See: https://stape.io/blog/safari-itp-update-limits-cookies-to-7-days-for-responses-from-3rd-party-ips
// See: https://webkit.org/blog/10218/full-third-party-cookie-blocking-and-more/
// See: https://www.ablecdp.com/blog/how-conversions-can-be-tracked-without-third-party-cookies
type VisitorCookie struct {
	Cookie    string `gorm:"primaryKey" json:"cookie"` // foriegn key to Visitor.ID
	HttpOnly  bool
	BelongsTo string // foriegn key to Visitor.ID
	CreatedAt time.Time
	// a cookie obly is ever written to the db once
	commited bool
}

func (vc *VisitorCookie) GetHeaderString(serverconfig string) string {
	srvconfig := app.GetConfig().GetServer(serverconfig)
	return fmt.Sprintf("%s_v=%s; SameSite=strict; Secure; Domain=%s", srvconfig.CookieOpts.CookiePrefix, vc.Cookie, srvconfig.Domain)
}

func (v *Visitor) NewVisitorCookie() (ret *VisitorCookie, err error) {
	var cookie string
	cookie, err = utils.GenerateBase64RandomString(32)
	if err != nil {
		return
	}
	v.initmodel()
	ret = &VisitorCookie{
		Cookie:    cookie,
		HttpOnly:  false,
		BelongsTo: v.ID,
		CreatedAt: time.Now(),
	}
	return
}

func (v *Visitor) NewVisitorSSCookie() (ret *VisitorCookie, err error) {
	var cookie string
	cookie, err = utils.GenerateBase64RandomString(32)
	if err != nil {
		return
	}
	v.initmodel()
	ret = &VisitorCookie{
		Cookie:    cookie,
		HttpOnly:  true,
		BelongsTo: v.ID,
		CreatedAt: time.Now(),
	}
	return
}

func (vc *VisitorCookie) Commit(db *gorm.DB) (ret error) {
	if !vc.commited {
		ret = db.Create(vc).Error
		if ret == nil {
			vc.commited = true
		}
	}
	return
}

// If we have a cookie string, but need an object.
// We will lookup in the DB to see if we have a cookie by this name, if we do not
// we will create a new cookie object and return it.
// NOTE: The new cookie object will not have the same cookie string as the one passed in.
func CookieFromCookieVal(db *gorm.DB, cookie string, v *Visitor) (cookiem *VisitorCookie, err error) {
	// lookup cookie by cookie string
	// the returned cookie must have a valid BelongsTo field which matches to passed in Visitor
	// if not a new cookie will be created
	var c VisitorCookie
	v.initmodel()
	err = db.Where("cookie = ?", cookie).Last(&c).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			model_debugf("CookieFromCookieVal: Cookie not found. Creating new cookie.")
			cookiem, err = v.NewVisitorCookie()
		}
	} else {
		if len(c.BelongsTo) < 1 || c.BelongsTo != v.ID {
			model_debugf("CookieFromCookieVal: Cookie found, but does not belong to visitor. Creating new cookie.")
			cookiem, err = v.NewVisitorCookie()
		} else {
			model_debugf("CookieFromCookieVal: Cookie found. Returning cookie. ID: %s ID: %s %d", v.ID, c.BelongsTo, len(c.BelongsTo))
			cookiem = &c
			cookiem.commited = true
		}
	}
	return
}

func SSCookieFromSSCookieVal(db *gorm.DB, cookie string, v *Visitor) (cookiem *VisitorCookie, err error) {
	// lookup cookie by cookie string
	// the returned cookie must have a valid BelongsTo field which matches to passed in Visitor
	// if not a new cookie will be created
	var c VisitorCookie
	v.initmodel()
	err = db.Where("cookie = ?", cookie).Last(&c).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			cookiem, err = v.NewVisitorSSCookie()
		}
	} else {
		if len(c.BelongsTo) < 1 || c.BelongsTo != v.ID {
			cookiem, err = v.NewVisitorCookie()
		} else {
			cookiem = &c
			cookiem.commited = true
		}
	}
	return
}

type Alias struct {
	Email     string `gorm:"primaryKey" json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	//	BelongsTo string // foriegn key to Visitor.ID
	//	Visitors  []*Visitor `gorm:"many2many:visitor_to_alias" json:"visitors"` // many:many
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Note: ^ is ` (backtick)
// see utils.SqlStr()
const (
	sqlCreateAlias = `
	CREATE TABLE IF NOT EXISTS aliases
	(
		^email^ String,
		^first_name^ String,
		^last_name^ String,
		^created_at^ DateTime64(3),
		^updated_at^ DateTime64(3)
	)
	ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY email;`

	sqlCreateVisitorToAlias = `
	CREATE TABLE IF NOT EXISTS visitor_to_alias
	(
		^email^ String,
		^visitor_id^ String,
		^created_at^ DateTime64(3),
		^updated_at^ DateTime64(3)
	)
	ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (email, visitor_id);`
)

// About many-to-many and one-to-many relationships w/ Clickhouse:
// Clickhouse does not have unique and foreign constraints.

type Visitor struct {
	HModel
	Email string `gorm:"unique" json:"email"`
	// server-side cookie
	//SSCookie     string
	FirstName    string           `json:"first_name"`
	LastName     string           `json:"last_name"`
	IPs          []IP             `gorm:"many2many:ip_list;" json:"ips"`                   // many:many
	Event        []Event          `gorm:"foreignKey:BelongsTo;references:ID"`              // 1:many
	Accepted     []AcceptedOnWeb  `gorm:"many2many:accepted_on_web_list;" json:"accepted"` // many:many
	VisitCookies []*VisitorCookie `gorm:"foreignKey:BelongsTo;references:ID"`              // 1:many
	// people may fill out forms with different emails
	Aliases []*Alias `gorm:"many2many:visitor_to_alias" json:"aliases"` // many:many

	// true if the struct has been modified since being pulled from the DB
	wasMod bool
}

const (
	sqlCreateVisitor = `
	CREATE TABLE IF NOT EXISTS visitors
	(
		^id^ String,
		^created_at^ DateTime64(3),
		^updated_at^ DateTime64(3),
		^email^ String,
		^ss_cookie^ String,
		^first_name^ String,
		^last_name^ String
	)
	ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY id;`
)

func (v *Visitor) BeforeCreate(tx *gorm.DB) (err error) {
	// UUID version 7
	if len(v.ID) < 1 {
		uuid7, err := uuid.NewV7()
		if err != nil {
			return err
		}
		v.ID = uuid7.String()
	}
	return
}

const sqlInsertVisitor = "INSERT INTO `visitors` (`id`,`created_at`,`updated_at`,`email`,`first_name`,`last_name`) VALUES (?,?,?,?,?,?)"

func (v *Visitor) initmodel() (err error) {
	if len(v.ID) < 1 {
		var uuid7 uuid.UUID
		uuid7, err = uuid.NewV7()
		if err != nil {
			return
		}
		v.ID = uuid7.String()
		v.wasMod = true
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now()
		v.wasMod = true
	}
	// if v.UpdatedAt.IsZero() {
	// 	v.UpdatedAt = time.Now()
	// 	v.wasMod = true
	// }
	return
}

func (v *Visitor) initmodelErrorIfOld() (err error) {
	if len(v.ID) < 1 {
		var uuid7 uuid.UUID
		uuid7, err = uuid.NewV7()
		if err != nil {
			return
		}
		v.ID = uuid7.String()
		v.wasMod = true
	} else {
		return fmt.Errorf("Visitor already has an ID: %s", v.ID)
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now()
		v.wasMod = true
	}
	// if v.UpdatedAt.IsZero() {
	// 	v.UpdatedAt = time.Now()
	// }
	return
}

func (v *Visitor) Commit(db *gorm.DB) error {
	v.initmodel()
	return db.Transaction(func(tx *gorm.DB) (err error) {
		if err = tx.Exec(sqlInsertVisitor, v.ID, v.CreatedAt.UnixMilli(), v.UpdatedAt.UnixMilli(), v.Email, v.FirstName, v.LastName).Error; err != nil {
			return err
		}
		return
		//return tx.Save(v).Error
		// if err = tx.Exec("INSERT INTO `visitor_to_alias` (`visitor_id`,`alias_email`) VALUES (?,?)", visitor.ID, alias.Email).Error; err != nil {
		// 	fmt.Printf("Error inserting into visitor_to_alias: %s", err.Error())
		// 	log.Errorf("Error inserting into visitor_to_alias: %s", err.Error())

		// }
		// return
	})
	// return db.Save(v).Error
}

func (v *Visitor) NewCommitSSCookie(db *gorm.DB) (sscookie string, err error) {
	sscookie, err = utils.GenerateBase64RandomString(32)
	if err != nil {
		return
	}
	if v.ID == "" {
		uuid7, err := uuid.NewV7()
		if err != nil {
			return "", err
		}
		v.ID = uuid7.String()
	}
	v.initmodel()
	ret := &VisitorCookie{
		Cookie:    sscookie,
		BelongsTo: v.ID,
		HttpOnly:  true,
		CreatedAt: time.Now(),
	}
	err = db.Model(ret).Create(ret).Error
	if err == nil {
		ret.commited = true
	}
	return
}

func (v *Visitor) NewCommitCookie(db *gorm.DB) (sscookie string, err error) {
	sscookie, err = utils.GenerateBase64RandomString(32)
	if err != nil {
		return
	}
	if v.ID == "" {
		uuid7, err := uuid.NewV7()
		if err != nil {
			return "", err
		}
		v.ID = uuid7.String()
	}
	v.initmodel()
	ret := &VisitorCookie{
		Cookie:    sscookie,
		BelongsTo: v.ID,
		HttpOnly:  false,
		CreatedAt: time.Now(),
	}
	err = db.Model(ret).Create(ret).Error
	if err == nil {
		ret.commited = true
	}
	return
}

// jsut returns a new visitor struct. Does not commit anything to the database
func NewVisitor() (ret *Visitor) {
	ret = &Visitor{}
	ret.initmodel()
	return
}

func (v *Visitor) FlagMod() {
	v.wasMod = true
}

func (v *Visitor) SetEmail(email string) {
	v.wasMod = true
	v.Email = email
}

func (v *Visitor) SetFirstName(firstname string) {
	v.wasMod = true
	v.FirstName = firstname
}

func (v *Visitor) SetLastName(lastname string) {
	v.wasMod = true
	v.LastName = lastname
}

// func (v *Visitor) GenID() string {
// 	v.ID, _ = utils.GenerateBase64RandomString(32)
// 	return v.ID
// }

func AutoMigrateVisitorModels(db *gorm.DB) error {
	// must call the create table raw SQL before AutoMigrate or
	// otherwise gorm will create them for you
	err := db.Exec(utils.SqlStr(sqlCreateAlias)).Error
	if err != nil {
		return err
	}
	err = db.Exec(utils.SqlStr(sqlCreateIP)).Error
	if err != nil {
		return err
	}

	err = db.Exec(utils.SqlStr(sqlCreateVisitor)).Error
	if err != nil {
		return err
	}
	err = db.Exec(utils.SqlStr(sqlCreateVisitorToAlias)).Error
	if err != nil {
		return err
	}
	// this should work, but by doing this it seems to change the
	// var ctx context.Context
	// also tried .WithContext(ctx)
	// err = db.Set("gorm:table_options", "ENGINE=ReplacingMergeTree ORDER BY id").AutoMigrate(&Visitor{})
	// if err != nil {
	// 	return err
	// }

	err = db.AutoMigrate(&IP{}, &Event{}, &AcceptedOnWeb{}, &VisitorCookie{})
	if err != nil {
		return err
	}

	//	err := db.Set()"gorm:table_options", "ENGINE=Distributed(cluster, default, hits)").AutoMigrate(&User{})
	return nil
}

func GetVisitorBySSCookie(db *gorm.DB, cookie string) (visitor *Visitor, err error) {
	var vc Visitor
	err = db.Model(&Visitor{}).Select("visitors.id, visitor_cookies.belongs_to").Joins("left join visitor_cookies on visitor_cookies.belongs_to = visitors.id").Where("(visitor_cookies.cookie = ?) AND (visitor_cookies.http_only = 1)", cookie).Scan(&vc).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			err = nil
		}
		return
	}
	// apparently this query will not return a gorm.ErrRecordNotFound even if there are no results
	if len(vc.ID) > 0 {
		visitor = &vc
	}
	return
}

// func (v *Visitor) CommitSSCookie(db *gorm.DB, cookie string) (err error) {
// 	v.SSCookie = cookie
// 	err = db.Model(v).Update("ss_cookie", cookie).Error
// 	return
// }

func GetVisitorByCookie(db *gorm.DB, cookie string) (visitor *Visitor, err error) {
	var vc Visitor
	// var ckies []VisitorCookie
	//	lookup := []string{cookie}

	//	err = db.Model(&vc).Where("cookie = ?", cookie).Association("VisitCookies").Error

	// can't figure out how to do it with Association
	err = db.Model(&Visitor{}).Select("visitors.id, visitor_cookies.belongs_to").Joins("left join visitor_cookies on visitor_cookies.belongs_to = visitors.id").Where("visitor_cookies.cookie = ?", cookie).Last(&vc).Error

	// OLD
	//	err = db.Model(&VisitorCookie{}).Where("cookie = ?", cookie).Association("VisitCookies").Find(&vc)
	//err = db.Model(&VisitorCookie{}).Where("cookie = ?", cookie).Association("VisitCookies").DB.First(&vc).Error
	//err = db.Model(&Visitor{}).Association("VisitCookies").Find(&vc, "cookie = ?", cookie)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			err = nil
		}
		return
	}
	if len(vc.ID) > 0 {
		visitor = &vc
	}
	return
}

// func AddCookieToVisitor(db *gorm.DB, visitor *Visitor, cookie *VisitorCookie) error {
// 	var httponlyval uint8
// 	if cookie.HttpOnly {
// 		httponlyval = 1
// 	}
// 	return db.Transaction(func(tx *gorm.DB) (err error) {
// 		if err = tx.Exec("INSERT INTO `visitor_cookies` (`cookie`,`http_only`,`belongs_to`) VALUES (?,?,?)", cookie.Cookie, httponlyval, visitor.ID).Error; err != nil {
// 			log.Errorf("Error inserting into visitor_cookies: %s", err.Error())
// 		}
// 		return err
// 	})

// 	// return db.Model(visitor).Association("VisitCookies").Append(cookie)
// }

func AddVisitor(db *gorm.DB, visitor *Visitor) error {
	return db.Create(visitor).Error
}

func GetVisitorByEmail(db *gorm.DB, email string) (visitor *Visitor, err error) {
	var v Visitor
	err = db.Where("email = ?", email).Last(&v).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			err = nil
		}
		return
	}
	visitor = &v
	return
}

func GetVisitorByID(db *gorm.DB, id string) (visitor *Visitor, err error) {
	var v Visitor
	err = db.Where("id = ?", id).Last(&v).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			err = nil
		}
		return
	}
	visitor = &v
	return
}

func GetEventsByVisitor(db *gorm.DB, visitor *Visitor) (events []Event, err error) {
	err = db.Model(visitor).Association("Event").Find(&events)
	return
}

func UpsertAliasForVisitor(db *gorm.DB, visitor *Visitor, alias *Alias) (err error) {
	// err := db.Model(alias).Create(alias).Commit().Error
	// if err != nil {
	// 	return err
	// }
	//	db.Connection().Exec("INSERT INTO alias (email, first_name, last_name) VALUES (?, ?, ?) ON CONFLICT (email) DO UPDATE SET first_name = ?, last_name = ?", alias.Email, alias.FirstName, alias.LastName, alias.FirstName, alias.LastName

	//	return db.Model(visitor).Association("Aliases").Append(alias)
	// sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
	// 	// do some database operations in the transaction (use 'tx' from this point, not 'db')
	// 	tx.Create(alias)
	// 	//		tx.Exec("INSERT INTO `visitor_to_alias` (`visitor_id`,`alias_email`) VALUES (?,?)", visitor.ID, alias.Email)

	// 	// return nil will commit the whole transaction
	// 	return tx
	// })

	// fmt.Printf("SQL: %s", sql)

	// sql = db.ToSQL(func(tx *gorm.DB) *gorm.DB {
	// 	// do some database operations in the transaction (use 'tx' from this point, not 'db')
	// 	tx.Model(visitor).Association("Aliases").Append(alias)
	// 	//		tx.Exec("INSERT INTO `visitor_to_alias` (`visitor_id`,`alias_email`) VALUES (?,?)", visitor.ID, alias.Email)

	// 	// return nil will commit the whole transaction
	// 	return tx
	// })

	// fmt.Printf("SQL: %s", sql)

	// since this table is ENGINE ReplacingMergeTree - and since the ORDER BY is email
	// we can be assured that even if a duplicate email is inserted, it will be merged down into
	// one eventually
	err = db.Model(alias).Create(alias).Error
	if err != nil {
		return err
	}
	// FAILS:
	//return db.Model(visitor).Association("Aliases").Append(alias)

	// i dunno - can't get their way to work: https://github.com/go-gorm/clickhouse/issues/147
	return db.Transaction(func(tx *gorm.DB) (err error) {
		if err = tx.Exec("INSERT INTO `visitor_to_alias` (`visitor_id`,`email`) VALUES (?,?)", visitor.ID, alias.Email).Error; err != nil {
			fmt.Printf("Error inserting into visitor_to_alias: %s", err.Error())
			log.Errorf("Error inserting into visitor_to_alias: %s", err.Error())
		}
		return err
	})
}
