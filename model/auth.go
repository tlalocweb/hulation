package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/utils"
	"gorm.io/gorm"
)

const (
	CapCreateVisitor = "create.visitor"
	CapUpdateVisitor = "update.visitor"
	CapDeleteVisitor = "delete.visitor"
	CapCreateForm    = "create.form"
	CapCreateAny     = "create.*" // create any object in the db
	Admin            = "admin"    // onmipotent - except can't delete root or admins
	Root             = "root"     // onmipotent. There is only 1
)

func ConcatCaps(caps ...string) string {
	return strings.Join(caps, ",")
}

func GetCaps(caps string) []string {
	return strings.Split(caps, ",")
}

type JWTClaims struct {
	Id          string   // user id
	LoginToken  string   `json:"t"`              // the login token - which is a UUID also, but has an associated expiration in the database along with a user id which should match the 'id' in the claim
	Caps        []string `json:"c"`              // map of capabilities
	TotpPending bool     `json:"tp,omitempty"`   // true if this is a limited token pending TOTP validation
	jwt.RegisteredClaims
}

func newJWTClaims(db *gorm.DB, userid string, token string, expiration time.Duration, opts *LoginOpts) (ret *jwt.Token, err error) {
	claims := &JWTClaims{
		Id:         userid,
		LoginToken: token,
		RegisteredClaims: jwt.RegisteredClaims{
			// In JWT, the expiry time is expressed as unix milliseconds
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiration)),
		},
	}
	ret = jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// look up roles based on user id
	attrs, err := GetAttrsByUserId(db, claims.Id)
	if err != nil {
		err = fmt.Errorf("error GetAttrsByUserId: %w", err)
		return
	}

	mapcaps := make(map[string]bool, len(attrs))
	if len(attrs) > 0 {
		for _, attr := range attrs {
			mapcaps[attr.Caps] = true
		}
	}
	roles, err := GetRolesByUserId(db, claims.Id)
	if err != nil {
		err = fmt.Errorf("error GetRolesByUserId: %w", err)
	}
	if len(roles) > 0 {
		for _, role := range roles {
			caps := GetCaps(role.Caps)
			for _, cap := range caps {
				mapcaps[cap] = true
			}
		}
	}
	claims.Caps = make([]string, 0, len(mapcaps))
	for k := range mapcaps {
		claims.Caps = append(claims.Caps, k)
	}
	if opts != nil {
		if opts.IsAdmin {
			claims.Caps = append(claims.Caps, "admin")
		}
	}
	return
}

type LoginOpts struct {
	IsAdmin     bool
	TotpPending bool
}

// NewTotpPendingToken creates a short-lived JWT (5 min) with totp_pending=true.
// This token can only be used to validate TOTP, not to access other APIs.
func NewTotpPendingToken(db *gorm.DB, userid string) (string, error) {
	dur := 5 * time.Minute
	token, err := CreateNewLoginToken(db, userid, time.Now().Add(dur))
	if err != nil {
		return "", fmt.Errorf("error creating totp pending login token: %w", err)
	}
	claims := &JWTClaims{
		Id:          userid,
		LoginToken:  token.ID,
		Caps:        []string{"totp_pending"},
		TotpPending: true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(dur)),
		},
	}
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return jwtToken.SignedString([]byte(app.GetConfig().JWTKey))
}

// IsTotpPendingToken returns true if the given claims represent a totp-pending token.
func IsTotpPendingToken(claims *JWTClaims) bool {
	return claims.TotpPending
}

// creates a new JWT. Commits the token inside the JWT to the database
// not returning the token until a successful commit
func NewJWTClaimsCommit(db *gorm.DB, userid string, opts *LoginOpts) (ret string, err error) {
	var dur time.Duration
	dur, err = time.ParseDuration(app.GetConfig().JWTExpiration)
	if err != nil {
		err = fmt.Errorf("error parsing duration: %w", err)
		return
	}
	// first create a login token
	var token *LoginToken
	token, err = CreateNewLoginToken(db, userid, time.Now().Add(dur))
	if err != nil {
		err = fmt.Errorf("error creating login token: %w", err)
		return
	}
	var jwt *jwt.Token
	jwt, err = newJWTClaims(db, userid, token.ID, dur, opts)
	if err != nil {
		err = fmt.Errorf("error creating JWT: %w", err)
		return
	}
	// Sign and get the complete encoded token as a string using the secret
	ret, err = jwt.SignedString([]byte(app.GetConfig().JWTKey))
	return
}

type UserPermissions struct {
	UserID string
	// Roles   []string
	// Attr    []string
	mapcaps  map[string]bool
	capslist []string
}

// looks at the capabilities of the UserPermissions
// object and returns if the capability is present
// either in the roles or the attributes
func (p *UserPermissions) HasCap(cap string) bool {
	return p.mapcaps[cap]
}

func (p *UserPermissions) ListCaps() []string {
	return p.capslist
}

// Takes an incoming JWT, verifies it is valid and then provides a  UserPermissions struct
// which tells us the user's ID and the roles / attrs it has
func VerifyJWTClaims(db *gorm.DB, token string) (valid bool, perms *UserPermissions, err error) {
	claims := &JWTClaims{}

	tkn, err := jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (any, error) {
		return []byte(app.GetConfig().JWTKey), nil
	})
	if err != nil {
		err = fmt.Errorf("error parsing JWT: %w", err)
		return
	}
	valid = tkn.Valid
	if tkn.Valid {
		var userid string
		userid, err = LookupLoginToken(db, claims.LoginToken)
		if err != nil {
			err = fmt.Errorf("error LookupLoginToken: %w", err)
			valid = false
			return
		}
		if userid != claims.Id {
			err = fmt.Errorf("user id does not match login token")
			valid = false
			return
		}
	} else {
		err = fmt.Errorf("token not valid")
		return
	}
	perms = &UserPermissions{}
	perms.mapcaps = make(map[string]bool, len(claims.Caps))
	for _, cap := range claims.Caps {
		perms.mapcaps[cap] = true
	}
	perms.capslist = claims.Caps
	perms.UserID = claims.Id
	return
}

type Role struct {
	Name      string `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	Caps      string          // comma separated list of capabilities
	mapcaps   map[string]bool // map of capabilities for quick lookup
}

func (r *Role) HasCap(cap string) bool {
	if r.mapcaps == nil {
		r.mapcaps = make(map[string]bool)
		caps := GetCaps(r.Caps)
		for _, c := range caps {
			r.mapcaps[c] = true
		}
	}
	return r.mapcaps[cap]
}

const (
	sqlCreateRole = `
	CREATE TABLE IF NOT EXISTS roles
	(
		^name^ String,
		^caps^ String,
		^created_at^ DateTime64(3),
		^updated_at^ DateTime64(3),
	)
	ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (name);`

	sqlCreateUserToRole = `
	CREATE TABLE IF NOT EXISTS user_to_role
	(
		^role^ String,
		^user_id^ String,
		^created_at^ DateTime64(3),
	)
	ENGINE = ReplacingMergeTree(created_at)
	ORDER BY (role, user_id);`
)

type UserAttr struct {
	CreatedAt time.Time
	UserId    string
	Caps      string // should only have capability in it
}

const (
	sqlCreateAttr = `
	CREATE TABLE IF NOT EXISTS user_attrs
	(
		^user_id^ String,
		^caps^ String,
		^created_at^ DateTime64(3),
	)
	ENGINE = ReplacingMergeTree(created_at)
	ORDER BY (user_id, caps);`
)

// Creates a Role object and commits it to the database
func CreateRole(db *gorm.DB, name string, caps string) (*Role, error) {
	role := &Role{
		Name: name,
		Caps: caps,
	}
	// check if role exists
	var existing *Role
	existing, err := GetRoleByName(db, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("role with name %s already exists", name)
	}
	err = db.Create(role).Error
	if err != nil {
		return nil, err
	}
	return role, nil
}

func DeleteRole(db *gorm.DB, name string) error {
	err := db.Delete(&Role{}, "name = ?", name).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("role not found: %w", err)
		}
		return err
	}
	// optimize table to actually delete the records
	return db.Exec("OPTIMIZE TABLE roles").Error
}

func GetRoleByName(db *gorm.DB, name string) (*Role, error) {
	role := new(Role)
	err := db.Where("name = ?", name).First(role).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return role, nil
}

func AddAttrToUser(db *gorm.DB, userid string, attr string) error {
	return db.Create(&UserAttr{
		UserId: userid,
		Caps:   attr,
	}).Error
}

func RemoveAttrFromUser(db *gorm.DB, userid string, attr string) (err error) {
	err = db.Delete(&UserAttr{}, "user_id = ? AND caps = ?", userid, attr).Error
	if err != nil {
		return
	}
	// optimize table
	err = db.Exec("OPTIMIZE TABLE user_attrs").Error
	return
}

func AddRoleToUser(db *gorm.DB, userid string, role string) error {
	return db.Exec("INSERT INTO user_to_role (role, user_id, created_at) VALUES (?, ?, ?)", role, userid, time.Now()).Error
}

func DeleteRoleFromUser(db *gorm.DB, userid string, role string) (err error) {
	err = db.Exec("DELETE FROM user_to_role WHERE role = ? AND user_id = ?", role, userid).Error
	return err
}

func GetRolesByUserId(db *gorm.DB, userid string) ([]*Role, error) {
	var ret []*Role
	// err := db.Where("user_id = ?", userid).Find(&ret).Error
	// err = db.Model(&Visitor{}).Select("visitors.id, visitor_cookies.belongs_to").Joins(
	// 	"left join visitor_cookies on visitor_cookies.belongs_to = visitors.id").Where(
	// 	"(visitor_cookies.cookie = ?) AND (visitor_cookies.http_only = 1)", cookie).Scan(&vc).Error
	err := db.Model(&Role{}).Select("roles.*").Joins("left join user_to_role on user_to_role.role = roles.name").Where(
		"user_to_role.user_id = ?", userid).Scan(&ret).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return ret, nil
}

func GetAttrsByUserId(db *gorm.DB, userid string) ([]*UserAttr, error) {
	var ret []*UserAttr
	err := db.Where("user_id = ?", userid).Find(&ret).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return ret, nil
}

const (
	sqlCreateUser = `
	CREATE TABLE IF NOT EXISTS users
	(
		^id^ String,
		^created_at^ DateTime64(3),
		^updated_at^ DateTime64(3),
		^email^ String,
		^first_name^ String,
		^last_name^ String,
	)
	ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (id);`
)

type User struct {
	HModel
	Email string `gorm:"index"`
	//	Roles     []*Role `gorm:"many2many:user_roles;"`
	FirstName string
	LastName  string
}

func (u *User) BeforeCreate(tx *gorm.DB) (err error) {
	// UUID version 7
	if len(u.ID) < 1 {
		uuid7, err := uuid.NewV7()
		if err != nil {
			return err
		}
		u.ID = uuid7.String()
	}
	return
}

func CreateNewUser(db *gorm.DB, email string, first string, last string) (*User, error) {
	user := &User{
		Email:     email,
		FirstName: first,
		LastName:  last,
	}
	// verify no user has this email
	var existing *User
	existing, err := GetUserByEmail(db, email)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("user with email %s already exists", email)
	}
	err = db.Create(user).Error
	if err != nil {
		return nil, err
	}
	return user, nil
}

func DeleteUser(db *gorm.DB, id string) error {
	err := db.Delete(&User{}, "id = ?", id).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("user not found: %w", err)
		}
		return err
	}
	// optimize table to actually delete the records
	return db.Exec("OPTIMIZE TABLE users").Error
}

func DeleteUserByEmail(db *gorm.DB, email string) error {
	err := db.Delete(&User{}, "email = ?", email).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("user not found: %w", err)
		}
		return err
	}
	// optimize table to actually delete the records
	return db.Exec("OPTIMIZE TABLE users").Error
}

func GetUserByEmail(db *gorm.DB, email string) (*User, error) {
	user := new(User)
	err := db.Where("email = ?", email).First(user).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return user, nil
}

func GetUserById(db *gorm.DB, id string) (*User, error) {
	user := new(User)
	err := db.Where("id = ?", id).First(user).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return user, nil
}

const (
	sqlCreateAPIKey = `
	CREATE TABLE IF NOT EXISTS api_keys
	(
		^key^ String,
		^user_id^ String,
		^api_functions^ String,
		^first_seen^ DateTime64(3),
		^last_seen^ DateTime64(3),
		^expiration^ DateTime64(3),
		^seen^ Int32,
	)
	ENGINE = ReplacingMergeTree(last_seen)
	ORDER BY (key);`
)

type APIKey struct {
	Key    string `gorm:"primaryKey" json:"apikey"`
	UserId string
	// Allowed API functions
	APIFunctions string // comma seperated list of functions
	FirstSeen    time.Time
	LastSeenAt   time.Time
	Expiration   time.Time
	Seen         int // how many times this IP has been seen
	// Events       []*AuthEvent `gorm:"foreignKey:FromAPIKey;references:Key"`
}

type AuthEvent struct {
	HModel
	FromAPIKey string
	FromToken  string
	Code       int
	FromIP     string
	FromUser   string
}

type LoginToken struct {
	ID        string `gorm:"primaryKey"`
	UserId    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

var ErrTokenExpired = fmt.Errorf("token expired")
var ErrTokenInvalid = fmt.Errorf("token not valid")

func CreateNewLoginToken(db *gorm.DB, userid string, expiration time.Time) (token *LoginToken, err error) {
	var uuid7 uuid.UUID
	uuid7, err = uuid.NewV7()
	if err != nil {
		return
	}
	token = &LoginToken{
		ID:        uuid7.String(),
		UserId:    userid,
		ExpiresAt: expiration,
	}
	err = db.Create(token).Error
	if err != nil {
		return nil, err
	}
	return token, nil
}

func LookupLoginToken(db *gorm.DB, token string) (userid string, err error) {

	// TODO add caching

	var logintoken LoginToken
	err = db.Where("id = ?", token).First(&logintoken).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			err = ErrTokenInvalid
		}
		return
	}
	if time.Now().After(logintoken.ExpiresAt) {
		err = ErrTokenExpired
		err2 := db.Delete(&LoginToken{}, "id = ?", token).Error
		if err2 != nil {
			err = fmt.Errorf("token expired and could not be deleted: %w", err2)
		}
		return
	}
	userid = logintoken.UserId
	return
}

func VerifyLoginToken(db *gorm.DB, token string, checkid string) (ok bool, err error) {
	var userid string
	userid, err = LookupLoginToken(db, token)
	if err != nil {
		if err == ErrTokenExpired {
			ok = false
			err = nil
		}
		if err == ErrTokenInvalid {
			ok = false
			err = nil
		}
		return
	}
	ok = (userid == checkid)
	return
}

func CreateNewAPIKey(db *gorm.DB, key string, expiration time.Time) (*APIKey, error) {
	apikey := &APIKey{
		Key:        key,
		Expiration: expiration,
	}
	err := db.Create(apikey).Error
	if err != nil {
		return nil, err
	}
	return apikey, nil
}

func AutoMigrateAuthModels(db *gorm.DB) (err error) {
	// // must call the create table raw SQL before AutoMigrate or
	// // otherwise gorm will create them for you
	err = db.Exec(utils.SqlStr(sqlCreateUser)).Error
	if err != nil {
		return fmt.Errorf("error creating user table: %w", err)
	}
	err = db.Exec(utils.SqlStr(sqlCreateRole)).Error
	if err != nil {
		return fmt.Errorf("error creating role table: %w", err)
	}

	err = db.Exec(utils.SqlStr(sqlCreateUserToRole)).Error
	if err != nil {
		return fmt.Errorf("error creating user_to_role table: %w", err)
	}

	err = db.Exec(utils.SqlStr(sqlCreateAPIKey)).Error
	if err != nil {
		return fmt.Errorf("error creating api_keys table: %w", err)
	}

	err = db.Exec(utils.SqlStr(sqlCreateAttr)).Error
	if err != nil {
		return fmt.Errorf("error creating user_attrs table: %w", err)
	}

	err = db.AutoMigrate(&AuthEvent{}, &LoginToken{})
	if err != nil {
		return fmt.Errorf("error auto migrating: %w", err)
	}

	//	err := db.Set()"gorm:table_options", "ENGINE=Distributed(cluster, default, hits)").AutoMigrate(&User{})
	return nil
}
