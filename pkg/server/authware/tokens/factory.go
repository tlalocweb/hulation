package tokens

import (
	"fmt"
	"sync"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	loglib "github.com/tlalocweb/hulation/log"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"github.com/tlalocweb/hulation/pkg/server/authware/permissions"
	permcache "github.com/tlalocweb/hulation/pkg/server/authware/permissions/cache"
	storecommon "github.com/tlalocweb/hulation/pkg/store/common"
	"github.com/tlalocweb/hulation/pkg/utils"
)

var log *loglib.TaggedLogger

func init() {
	log = loglib.GetTaggedLogger("JWTFAC", "JWT Factory")
}

type JWTFactory struct {
	keyIndex             int
	mu                   sync.RWMutex
	numKeys              int
	keyDuration          time.Duration
	tokenDuration        time.Duration
	lastKeyUpdate        time.Time
	storage              storecommon.Storage
	keyStorePrefix       string
	userTokenStorePrefix string
	cacheKeys            map[string]*apiobjects.StoredTokenKey
	latestCachedKey      *apiobjects.StoredTokenKey
	keys                 jwt.VerificationKeySet
	DebugLevel           int
}

type JWTFactoryOpts struct {
	TokenDuration time.Duration
}

func NewJWTFactoryOpts() *JWTFactoryOpts {
	return &JWTFactoryOpts{
		TokenDuration: time.Hour * 24,
	}
}

func OptionFactoryTokenDuration(d time.Duration) func(*JWTFactoryOpts) {
	return func(o *JWTFactoryOpts) {
		o.TokenDuration = d
	}
}

func NewJWTFactory(numKeys int, keyDuration time.Duration, keystoreprefix string, userTokenStorePrefix string, storage storecommon.Storage, opts ...func(*JWTFactoryOpts)) *JWTFactory {
	factory := &JWTFactory{
		keyStorePrefix:       keystoreprefix,
		userTokenStorePrefix: userTokenStorePrefix,
		numKeys:             numKeys,
		keyDuration:         keyDuration,
		storage:             storage,
	}
	finalopts := NewJWTFactoryOpts()
	for _, o := range opts {
		o(finalopts)
	}
	factory.tokenDuration = finalopts.TokenDuration
	return factory
}

func (f *JWTFactory) LoadAllKeys() (err error) {
	var totalkeys int
	log.Debugf("got LoadAllKeys()")
	newcache := make(map[string]*apiobjects.StoredTokenKey)
	keyset := jwt.VerificationKeySet{}
	var latestkey *apiobjects.StoredTokenKey
	m, err2 := f.storage.GetObjects(f.keyStorePrefix)
	if err2 != nil {
		err = fmt.Errorf("failed to get token keys: %v", err2)
		return
	}
	for k, v := range m {
		tokenkey, ok := v.(*apiobjects.StoredTokenKey)
		if !ok {
			log.Errorf("error casting object to TokenKey")
			continue
		}
		if tokenkey.Expires < time.Now().Unix() {
			log.Debugf("see expired key, ignoring: %s", k)
		} else {
			newcache[k] = tokenkey
			// Select the key with the highest Created timestamp as latestkey
			// This ensures newly created keys are used for signing
			if latestkey == nil || tokenkey.Created > latestkey.Created {
				latestkey = tokenkey
			}
			keyset.Keys = append(keyset.Keys, []byte(tokenkey.Key))
			log.Debugf("loaded key: %s (created: %d)", k, tokenkey.Created)
			totalkeys++
		}
	}

	if err != nil {
		log.Errorf("error rotating token keys: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = keyset
	f.cacheKeys = newcache
	if latestkey != nil {
		f.latestCachedKey = latestkey
	}
	return
}

func (f *JWTFactory) DeleteAllKeys() (err error) {
	return f.storage.DeleteAll(f.keyStorePrefix)
}

// IsInitialized checks if JWT keys have been successfully loaded
func (f *JWTFactory) IsInitialized() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.latestCachedKey != nil && len(f.cacheKeys) > 0
}

func (f *JWTFactory) RotateKeys() {
	var totalkeys int
	var err error
	log.Debugf("got RotateKeys()")
	newcache := make(map[string]*apiobjects.StoredTokenKey)
	keyset := jwt.VerificationKeySet{}
	var latestkey *apiobjects.StoredTokenKey

	// Load existing keys using GetObjects (similar to LoadAllKeys pattern)
	m, err := f.storage.GetObjects(f.keyStorePrefix)
	if err != nil {
		log.Errorf("failed to get token keys: %v", err)
		return
	}

	// Track keys to delete
	var keysToDelete []string

	totalkeys = len(m)
	for k, v := range m {
		log.Debugf("checking key: %s", k)
		tokenkey, ok := v.(*apiobjects.StoredTokenKey)
		if !ok {
			log.Errorf("error casting object to TokenKey")
			continue
		}
		if tokenkey.Expires < time.Now().Unix() {
			log.Debugf("marking expired key for deletion: %s", k)
			keysToDelete = append(keysToDelete, f.keyStorePrefix+k)
			totalkeys--
		} else {
			newcache[k] = tokenkey
			// Select the key with the highest Created timestamp as latestkey
			if latestkey == nil || tokenkey.Created > latestkey.Created {
				latestkey = tokenkey
			}
			keyset.Keys = append(keyset.Keys, []byte(tokenkey.Key))
			log.Debugf("loaded key: %s (created: %d)", k, tokenkey.Created)
		}
		if totalkeys > f.numKeys {
			log.Warnf("total keys (%d) >= numKeys (%d), marking key for deletion", totalkeys, f.numKeys)
			keysToDelete = append(keysToDelete, f.keyStorePrefix+k)
			totalkeys--
		}
	}

	// Delete expired/excess keys
	for _, k := range keysToDelete {
		err = f.storage.Delete(k)
		if err != nil {
			log.Errorf("error deleting key %s: %v", k, err)
		} else {
			log.Debugf("deleted key: %s", k)
		}
	}

	// Generate new keys if needed
	for totalkeys < f.numKeys {
		newrand, err := utils.GenerateBase64RandomStringNoPadding(64)
		if err != nil {
			log.Errorf("error generating new key: %v", err)
			break
		}
		now := time.Now()
		expires := now.Add(f.keyDuration)
		newk := &apiobjects.StoredTokenKey{
			Key:     newrand,
			Expires: expires.Unix(),
			Created: now.Unix(),
		}
		log.Debugf("adding new token key (expires %s): %s", expires.Format(time.RFC1123), newrand)

		// Store the new key
		wrapped := apiobjects.SerWrapTokenKey(newk)
		err = f.storage.PutObject(f.keyStorePrefix+newrand, wrapped)
		if err != nil {
			log.Errorf("error storing new key: %v", err)
			break
		}

		latestkey = newk
		keyset.Keys = append(keyset.Keys, []byte(newrand))
		newcache[newrand] = newk
		totalkeys++
	}

	if err != nil {
		log.Errorf("error rotating token keys: %v %s", err, log.Loc(log.StackLevel(1)))
	}

	// Update in-memory cache
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = keyset
	f.cacheKeys = newcache
	if latestkey != nil {
		f.latestCachedKey = latestkey
	}
	log.Debugf("RotateKeys completed: %d keys in cache", len(f.cacheKeys))
}

func (f *JWTFactory) GenerateNewTokenForRoot(rootuserkey string, ip string) (token string, tokenid string, err error) {
	storeopts := make([]func(*storecommon.StoreOpts), 0)
	if f.DebugLevel > 0 {
		storeopts = append(storeopts, storecommon.OptionDebuglevel(f.DebugLevel))
	}
	storeopts = append(storeopts, storecommon.OptionAlwaysSetVal())
	storeopts = append(storeopts, storecommon.OptionOrCreate())

	tokenid = uuid.New().String()

	var tokenversion int64
	err = f.storage.MutateObject(rootuserkey, func(k string, obj *storecommon.Unwrappable) (error, bool) {
		err = obj.Unwrap()
		if err != nil {
			return err, false
		}
		var ok bool
		var rootuser *apiobjects.RootUser
		if obj.Obj() != nil {
			rootuser, ok = obj.Obj().(*apiobjects.RootUser)
			if !ok {
				return fmt.Errorf("error casting root user"), false
			}
		} else {
			rootuser = &apiobjects.RootUser{}
		}
		rootwrap := apiobjects.SerWrapRootUser(rootuser)
		rootwrap.UpdateLastTokenVersion()
		tokenversion = rootwrap.GetLastGoodTokenVersion()
		obj.Rewrap(rootwrap)
		return nil, false
	}, storeopts...)

	sess := apiobjects.NewSessionForRoot()
	sess.AddToken(tokenid)

	if err != nil {
		log.Errorf("Root user token not updated. MutateObject failed: %v", err)
		err = fmt.Errorf("token not updated. MutateObject failed: %w", err)
		return
	} else {
		log.Debugf("Root user token incremented")
	}

	if len(ip) == 0 {
		ip = "unknown"
	}

	claims := authware.GenClaimsForRoot(tokenid, tokenversion, f.tokenDuration, sess, ip)

	tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)

	f.mu.RLock()
	usekey := f.latestCachedKey
	f.mu.RUnlock()
	if usekey == nil {
		log.Errorf("lastCachedKey is nil")
		err = fmt.Errorf("lastCachedKey is nil")
	} else {
		token, err = tok.SignedString([]byte(usekey.Key))
	}

	return
}

func (f *JWTFactory) CreateToken(userin *apiobjects.User) (token string, tokenid string, err error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	storeopts := make([]func(*storecommon.StoreOpts), 0)
	if f.DebugLevel > 0 {
		storeopts = append(storeopts, storecommon.OptionDebuglevel(f.DebugLevel))
	}
	storeopts = append(storeopts, storecommon.OptionAlwaysSetVal())
	storeopts = append(storeopts, storecommon.OptionOrCreate())

	tokenid = uuid.New().String()

	var userout *apiobjects.User

	// Extract provider name from AuthProviderRef
	providerName := ""
	if userin.AuthProvider != nil {
		providerName = userin.AuthProvider.ProviderName
	}

	err = apiobjects.CreateOrOverwriteUserByIdentity(userin.ExternalIdentity, providerName, func(user *apiobjects.User, isnew bool) (err error, dodel bool) {
		// NOTE: GoodTokenVersion field removed from new schema
		// Token versioning needs to be reimplemented using a different approach
		// if isnew {
		// 	user.GoodTokenVersion = 0
		// } else {
		// 	user.GoodTokenVersion++
		// }
		// user.UpdateLastTokenLookup()
		// user.UpdateGoodTokenVersion()
		userout = user
		return
	})

	sess := apiobjects.NewSessionForUser(userin)
	sess.AddToken(tokenid)

	if err != nil {
		log.Errorf("Root user token not updated. MutateObject failed: %v", err)
		err = fmt.Errorf("token not updated. MutateObject failed: %w", err)
		return
	} else {
		log.Debugf("Root user token incremented")
	}

	// NOTE: Using tokenid (not userin.Identity), tokenversion=0 (GoodTokenVersion removed), and empty IP string (FromIp field removed)
	claims := authware.GenClaimsForUser(userout, tokenid, 0, f.tokenDuration, sess, "")

	// Resolve and store permissions, get checksum for JWT
	if permcache.IsInitialized() {
		// Resolve effective permissions from user's role assignments and direct permissions
		allowPerms, denyPerms := permissions.ResolveEffectivePermissions(
			userout.RoleAssignments,
			userout.DirectPermissions,
			userout.DirectDenyPermissions,
			nil, // group permissions - TODO: implement when groups are added
			nil, // group deny permissions
		)

		// Store in permission cache and get checksum
		cacheStore := permcache.GetStore()
		checksum, cacheErr := cacheStore.Store(userout.Uuid, allowPerms, denyPerms)
		if cacheErr != nil {
			log.Errorf("failed to store permissions in cache: %v", cacheErr)
			// Continue without checksum - middleware will use empty permissions
		} else {
			claims.PermissionsChecksum = checksum
			log.Debugf("Stored permissions for user %s: checksum=%016x, allow=%d, deny=%d",
				userout.Uuid, checksum, len(allowPerms), len(denyPerms))
		}
	} else {
		log.Debugf("Permission cache not initialized, skipping permission storage")
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)

	f.mu.RLock()
	usekey := f.latestCachedKey
	f.mu.RUnlock()
	if usekey == nil {
		log.Errorf("lastCachedKey is nil")
		return "", "", fmt.Errorf("lastCachedKey is nil")
	}

	token, err = tok.SignedString([]byte(usekey.Key))
	if err != nil {
		log.Errorf("failed to sign token: %v", err)
		return "", "", fmt.Errorf("failed to sign token: %w", err)
	}

	return token, tokenid, nil
}

func (f *JWTFactory) ValidateToken(tokenString string) (*authware.Claims, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Debug logging to diagnose signature validation issues
	log.Debugf("ValidateToken: keyset has %d keys, latestCachedKey is nil: %v",
		len(f.keys.Keys), f.latestCachedKey == nil)

	// Check if signing key is in validation keyset
	if f.latestCachedKey != nil {
		signingKeyBytes := []byte(f.latestCachedKey.Key)
		keyFound := false
		for i, k := range f.keys.Keys {
			if keyBytes, ok := k.([]byte); ok {
				if string(keyBytes) == string(signingKeyBytes) {
					keyFound = true
					log.Debugf("ValidateToken: signing key found at index %d in keyset", i)
					break
				}
			}
		}
		if !keyFound {
			log.Warnf("ValidateToken: signing key NOT FOUND in validation keyset! This will cause signature failures.")
		}
	}

	var claims authware.Claims
	token, err := jwt.ParseWithClaims(tokenString, &claims, func(token *jwt.Token) (interface{}, error) {
		return f.keys, nil
	})
	if err == nil && token.Valid {
		log.Infof("token signature validated: %v", claims)
		return &claims, nil
	}
	if err != nil {
		log.Debugf("error parsing token: %v", err)
	}
	return nil, fmt.Errorf("invalid token: %v", err)
}

func (f *JWTFactory) StoreUserToken(tk *apiobjects.StoredToken) (err error) {
	return fmt.Errorf("not implemented")
}

func (f *JWTFactory) LookupUserToken(tokenid string) (tk *apiobjects.StoredToken, err error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *JWTFactory) DeleteUserToken(tokenid string) (err error) {
	return fmt.Errorf("not implemented")
}

// CreateLimitedTokenForUser generates a limited JWT token for auth setup purposes
// This token has auth_setup_only=true and can only be used for WhoAmI, SetInitialPassword, and ListAuthProviders
// Used when user validates their email but hasn't completed auth setup (password or IdP)
func (f *JWTFactory) CreateLimitedTokenForUser(user *apiobjects.User) (token string, tokenid string, err error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	tokenid = uuid.New().String()

	sess := apiobjects.NewSessionForUser(user)
	sess.AddToken(tokenid)

	// Generate regular claims for the user
	claims := authware.GenClaimsForUser(user, tokenid, 0, time.Hour, sess, "") // 1 hour expiry for limited tokens

	// Mark as limited token
	claims.AuthSetupOnly = true

	tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)

	usekey := f.latestCachedKey
	if usekey == nil {
		log.Errorf("lastCachedKey is nil")
		return "", "", fmt.Errorf("lastCachedKey is nil")
	}

	token, err = tok.SignedString([]byte(usekey.Key))
	if err != nil {
		log.Errorf("failed to sign limited token: %v", err)
		return "", "", fmt.Errorf("failed to sign limited token: %w", err)
	}

	log.Debugf("Created limited token for user %s (auth_setup_only=true)", user.Uuid)
	return token, tokenid, nil
}

// CreateTotpPendingToken generates a limited JWT token for TOTP validation
// This token has totp_pending=true and can only be used for TotpValidate, TotpSetup, TotpVerifySetup, TotpStatus, and WhoAmI
// Used when user logs in with password but still needs to provide TOTP code
func (f *JWTFactory) CreateTotpPendingToken(user *apiobjects.User) (token string, tokenid string, err error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	tokenid = uuid.New().String()

	sess := apiobjects.NewSessionForUser(user)
	sess.AddToken(tokenid)

	// Generate regular claims for the user with 5-minute expiry (short-lived for security)
	claims := authware.GenClaimsForUser(user, tokenid, 0, 5*time.Minute, sess, "")

	// Mark as TOTP pending token
	claims.TotpPending = true

	tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)

	usekey := f.latestCachedKey
	if usekey == nil {
		log.Errorf("lastCachedKey is nil")
		return "", "", fmt.Errorf("lastCachedKey is nil")
	}

	token, err = tok.SignedString([]byte(usekey.Key))
	if err != nil {
		log.Errorf("failed to sign TOTP pending token: %v", err)
		return "", "", fmt.Errorf("failed to sign TOTP pending token: %w", err)
	}

	log.Debugf("Created TOTP pending token for user %s (totp_pending=true, expires in 5m)", user.Uuid)
	return token, tokenid, nil
}

// CreateRegistryUserToken (izcr-only) intentionally omitted: hulation has
// no RegistryUser / OCI authentication concept.
