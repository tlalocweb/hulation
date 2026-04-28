package authware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/open-policy-agent/opa/v1/rego"
	"github.com/tlalocweb/hulation/pkg/server/authware/permissions"
	permcache "github.com/tlalocweb/hulation/pkg/server/authware/permissions/cache"
	"github.com/tlalocweb/hulation/pkg/store/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// httpError sends an HTTP error response with proper Content-Length header.
// This is needed because http.Error doesn't set Content-Length, which causes
// HEAD requests to hang waiting for body data that will never come.
func httpError(w http.ResponseWriter, error string, code int) {
	body := []byte(error + "\n")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(code)
	w.Write(body)
}

type ContextKey string

const (
	ClaimsKey ContextKey = "claims-myri"
)

type DecodeTokenFunc func(jwt string) (claims *Claims, err error)

type Authware struct {
	store              common.Storage
	policy             string
	validateTokenFuncs []DecodeTokenFunc
	debuglevel         int
	queryAllow         rego.PreparedEvalQuery
	queryAllowPrepared bool
}

func NewAuthware(strg common.Storage, opapolicy string, checkTokenFuncs []DecodeTokenFunc, debuglevel int) (ware *Authware, err error) {
	// make sure we have precomputed the HTTP methods and paths for each defined gRPC method
	precomputeHTTPMethods()
	if opapolicy == "" || len(checkTokenFuncs) == 0 {
		err = errors.New("invalid parameters: policy and at least one token validation function required")
		return
	}
	ware = &Authware{
		store:              strg,
		policy:             opapolicy,
		validateTokenFuncs: checkTokenFuncs,
		debuglevel:         debuglevel,
	}
	return
}

func (w *Authware) checkPolicy(ctx context.Context, method, path string, claims *Claims) (bool, error) {
	return w.checkPolicyWithPermissions(ctx, method, path, claims, nil, false, nil)
}

// checkPolicyWithPermissions checks OPA policy with permission-based authorization.
// requiredPermTemplates are permission templates from proto annotations (e.g., "tenant.{tenant_uuid}.project.{project_uuid}.member.add")
// requestParams are used to resolve placeholders in permission templates
func (w *Authware) checkPolicyWithPermissions(ctx context.Context, method, path string, claims *Claims,
	requiredPermTemplates []string, requireAll bool, requestParams map[string]string) (bool, error) {
	log.Debugf("checkPolicy called with method=%s, path=%s, roles=%v, requiredPerms=%v", method, path, claims.Roles, requiredPermTemplates)

	var err error
	if !w.queryAllowPrepared {
		log.Debugf("checkPolicy: Preparing OPA query...")
		w.queryAllow, err = rego.New(
			rego.Query("data.izcr.authz.allow"),
			rego.Module("policy.rego", w.policy),
		).PrepareForEval(ctx)
		if err != nil {
			log.Errorf("checkPolicy: Failed to prepare OPA query: %v", err)
			return false, err
		}
		w.queryAllowPrepared = true
		log.Debugf("checkPolicy: OPA query prepared successfully")
	}

	// Load permission set from cache (keeps HashTrees intact for efficient matching)
	var permSet *permcache.PermissionSet
	var cacheStore permcache.PermissionCacheStore
	if claims.PermissionsChecksum != 0 && permcache.IsInitialized() {
		cacheStore = permcache.GetStore()
		var err2 error
		permSet, err2 = cacheStore.Get(claims.Subject, claims.PermissionsChecksum)
		if err2 != nil {
			log.Errorf("checkPolicy: Failed to load permissions from cache: %v", err2)
			return false, fmt.Errorf("failed to load permissions: %w", err2)
		}

		if permSet == nil {
			// Permissions not found - checksum mismatch (permissions changed since token was issued)
			log.Warnf("checkPolicy: Permissions stale for user %s (checksum %016x not found)",
				claims.Subject, claims.PermissionsChecksum)
			return false, &permcache.PermissionsStaleError{
				UserUUID:    claims.Subject,
				OldChecksum: claims.PermissionsChecksum,
				Message:     "PERMISSIONS_STALE",
			}
		}
		log.Debugf("checkPolicy: Loaded permission set from cache for user %s", claims.Subject)
	} else {
		// No checksum - this is likely an admin token or permission cache not initialized
		log.Debugf("checkPolicy: No permission checksum (checksum=%d, cacheInit=%v), skipping permission tree check",
			claims.PermissionsChecksum, permcache.IsInitialized())
	}

	// Resolve permission templates with actual request values
	var resolvedRequired []string
	permissionCheckPassed := false

	// If there are no required permissions, permission check trivially passes
	// (any authenticated user can access endpoints with no permission requirements)
	if len(requiredPermTemplates) == 0 {
		permissionCheckPassed = true
		log.Debugf("checkPolicy: No required permissions, permission check trivially passes")
	} else if requestParams != nil {
		resolvedRequired = make([]string, len(requiredPermTemplates))
		for i, template := range requiredPermTemplates {
			resolvedRequired[i] = permissions.ResolveRequiredPermission(template, requestParams)
		}
		log.Debugf("checkPolicy: Resolved permissions: %v -> %v", requiredPermTemplates, resolvedRequired)

		// Perform permission check using efficient HashTree matching (O(k) per permission)
		// No regex or string comparisons - uses radix tree MatchWithWildcards
		if permSet != nil && cacheStore != nil {
			if cacheStore.HasRequiredPermissions(permSet, resolvedRequired, requireAll) {
				permissionCheckPassed = true
				log.Debugf("checkPolicy: HashTree permission check passed")
			} else {
				log.Debugf("checkPolicy: HashTree permission check failed (required=%v, requireAll=%v)",
					resolvedRequired, requireAll)
			}
		} else {
			log.Debugf("checkPolicy: No permission set available, deferring to OPA role check")
		}
	}

	// Prepare input for OPA policy
	// Permission check is pre-computed using efficient HashTree matching
	input := map[string]interface{}{
		"method":                  method,
		"path":                    path,
		"roles":                   claims.Roles,
		"permissions":             claims.Permissions, // Legacy permissions field
		"permission_check_passed": permissionCheckPassed,
		"project_access":          claims.ProjectAccess,
		"user":                    claims.RegisteredClaims.Subject,
	}

	log.Debugf("checkPolicy: OPA policy input: method=%s, path=%s, roles=%v, requiredPerms=%v, requireAll=%v, permCheckPassed=%v, user=%s",
		method, path, claims.Roles, resolvedRequired, requireAll, permissionCheckPassed, claims.RegisteredClaims.Subject)

	rs, err := w.queryAllow.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		log.Errorf("checkPolicy: OPA policy evaluation error: %v", err)
		return false, err
	}

	log.Debugf("checkPolicy: OPA evaluation succeeded, result count=%d", len(rs))
	if len(rs) > 0 {
		log.Debugf("checkPolicy: Result value: %+v", rs[0].Expressions[0].Value)
		if rs[0].Expressions[0].Value == true {
			return true, nil
		}
	}

	log.Debugf("checkPolicy: Policy denied access (not allowed)")
	return false, nil
}

// extractRequestParams extracts parameter values from a protobuf request message.
// It looks for common fields like tenant_uuid, project_uuid that match placeholders
// in permission templates.
func extractRequestParams(req any) map[string]string {
	params := make(map[string]string)
	if req == nil {
		return params
	}

	v := reflect.ValueOf(req)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return params
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return params
	}

	// Common field names used in permission scoping
	fieldMappings := map[string]string{
		"TenantUuid":  "tenant_uuid",
		"ProjectUuid": "project_uuid",
		"UserUuid":    "user_uuid",
		"FleetUuid":   "fleet_uuid",
		"NodeUuid":    "node_uuid",
		"RolloutUuid": "rollout_uuid",
	}

	for structField, paramName := range fieldMappings {
		field := v.FieldByName(structField)
		if field.IsValid() && field.Kind() == reflect.String {
			val := field.String()
			if val != "" {
				params[paramName] = val
			}
		}
	}

	return params
}

// checkEmailValidation verifies that the user's email is validated, unless
// the endpoint is marked to skip email validation or the user is the admin.
// Returns an error if email validation is required but not present.
func (w *Authware) checkEmailValidation(grpcMethod string, claims *Claims) error {
	// Skip for endpoints marked with skip_email_validation
	if ShouldSkipEmailValidation(grpcMethod) {
		log.Debugf("Email validation skipped for method %s (annotation)", grpcMethod)
		return nil
	}

	// Admin (root) user is always exempt from email validation
	// Check both Username and Subject since root tokens use Subject="admin"
	if claims.Username == "admin" || claims.Subject == "admin" {
		log.Debugf("Email validation skipped for admin user")
		return nil
	}

	// Internal services are exempt from email validation
	if claims.IDProviderName == "certificate" {
		log.Debugf("Email validation skipped for internal service")
		return nil
	}

	// Registry users don't need email validation (they're service accounts)
	if claims.RegistryUserUUID != "" {
		log.Debugf("Email validation skipped for registry user")
		return nil
	}

	// Check if email is validated
	if !claims.IsEmailValidated() {
		log.Debugf("Email validation required but not present for user %s", claims.Username)
		return fmt.Errorf("email validation required")
	}

	log.Debugf("Email validation check passed for user %s", claims.Username)
	return nil
}

// checkAuthSetup verifies that the user has completed authentication setup
// (e.g., set a password or logged in via IdP at least once).
// Returns an error if auth setup is required but not complete.
func (w *Authware) checkAuthSetup(grpcMethod string, claims *Claims) error {
	// Skip for endpoints marked with skip_auth_setup_check
	if ShouldSkipAuthSetupCheck(grpcMethod) {
		log.Debugf("Auth setup check skipped for method %s (annotation)", grpcMethod)
		return nil
	}

	// Admin (root) user is always exempt from auth setup check
	if claims.Username == "admin" || claims.Subject == "admin" {
		log.Debugf("Auth setup check skipped for admin user")
		return nil
	}

	// Internal services are exempt from auth setup check
	if claims.IDProviderName == "certificate" {
		log.Debugf("Auth setup check skipped for internal service")
		return nil
	}

	// Registry users don't need auth setup (they're service accounts)
	if claims.RegistryUserUUID != "" {
		log.Debugf("Auth setup check skipped for registry user")
		return nil
	}

	// Check if auth setup is complete
	if claims.NeedsAuthSetup() {
		log.Debugf("Auth setup required but not complete for user %s", claims.Username)
		return fmt.Errorf("authentication setup required: please set a password or complete login")
	}

	log.Debugf("Auth setup check passed for user %s", claims.Username)
	return nil
}

// checkLimitedToken verifies that limited tokens (auth_setup_only) only access allowed endpoints
// Limited tokens can only access: WhoAmI, SetInitialPassword, ListAuthProviders
func (w *Authware) checkLimitedToken(grpcMethod string, claims *Claims) error {
	if !claims.IsAuthSetupToken() {
		// Not a limited token, allow all endpoints
		return nil
	}

	// Limited tokens can only access specific endpoints
	allowedMethods := map[string]bool{
		"/izcr.v1.auth.AuthService/WhoAmI":             true,
		"/izcr.v1.auth.AuthService/SetInitialPassword": true,
		"/izcr.v1.auth.AuthService/ListAuthProviders":  true,
	}

	if !allowedMethods[grpcMethod] {
		log.Debugf("Limited token rejected for method %s (only auth setup endpoints allowed)", grpcMethod)
		return fmt.Errorf("limited token: complete authentication setup first")
	}

	log.Debugf("Limited token allowed for method %s", grpcMethod)
	return nil
}

// checkTotpPendingToken verifies that TOTP pending tokens only access allowed endpoints
// TOTP pending tokens can only access: TotpValidate, TotpSetup, TotpVerifySetup, TotpStatus, WhoAmI
func (w *Authware) checkTotpPendingToken(grpcMethod string, claims *Claims) error {
	if !claims.IsTotpPendingToken() {
		// Not a TOTP pending token, allow all endpoints
		return nil
	}

	// TOTP pending tokens can only access specific endpoints
	allowedMethods := map[string]bool{
		"/izcr.v1.auth.AuthService/TotpValidate":   true,
		"/izcr.v1.auth.AuthService/TotpSetup":      true,
		"/izcr.v1.auth.AuthService/TotpVerifySetup": true,
		"/izcr.v1.auth.AuthService/TotpStatus":     true,
		"/izcr.v1.auth.AuthService/WhoAmI":         true,
	}

	if !allowedMethods[grpcMethod] {
		log.Debugf("TOTP pending token rejected for method %s (only TOTP endpoints allowed)", grpcMethod)
		return fmt.Errorf("TOTP verification required: please provide your TOTP code")
	}

	log.Debugf("TOTP pending token allowed for method %s", grpcMethod)
	return nil
}

func (ware *Authware) RegistryAuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debugf("Registry HTTP Handler: Received %s %s", r.Method, r.URL.Path)

		// Build proper WWW-Authenticate header with full URL for Docker compatibility
		// Docker expects: Bearer realm="https://host/token",service="registry"
		wwwAuthHeader := fmt.Sprintf(`Bearer realm="https://%s/token",service="registry"`, r.Host)

		// Allow unauthenticated access to /v2/ (version check) and /token (auth endpoint)
		// These endpoints are required for the Docker authentication flow:
		// 1. Docker hits /v2/ -> gets 401 with WWW-Authenticate header
		// 2. Docker extracts realm URL and hits /token with Basic auth
		// 3. Server returns Bearer token
		// 4. Docker uses Bearer token for subsequent requests
		if r.URL.Path == "/v2/" || r.URL.Path == "/token" || r.URL.Path == "/auth/token" {
			log.Debugf("Registry Handler: Allowing unauthenticated access to %s", r.URL.Path)
			next.ServeHTTP(w, r)
			return
		}

		// Extract the token from the Authorization header
		authHeader := r.Header.Get("Authorization")
		log.Debugf("Registry Handler: Authorization header present: %v, starts with Bearer: %v", authHeader != "", strings.HasPrefix(authHeader, "Bearer "))
		if authHeader == "" {
			// No auth header - return 401 with WWW-Authenticate to trigger Docker token flow
			log.Debugf("Registry Handler: No Authorization header, returning 401 with challenge")
			w.Header().Set("WWW-Authenticate", wwwAuthHeader)
			httpError(w, "Authorization header missing", http.StatusUnauthorized)
			return
		}

		// Check if this is a Bearer token
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenString := strings.TrimPrefix(authHeader, "Bearer ")
			// Validate the JWT token using existing token validators
			log.Debugf("Registry Handler: Validating JWT Bearer token")
			claims, err := ware.validateToken(tokenString)
			if err != nil {
				log.Errorf("Registry Handler: Token validation failed: %v", err)
				w.Header().Set("WWW-Authenticate", wwwAuthHeader)
				httpError(w, fmt.Sprintf("Token validation failed: %v", err), http.StatusUnauthorized)
				return
			}

			if claims == nil {
				log.Errorf("Registry Handler: Claims are nil after validation")
				w.Header().Set("WWW-Authenticate", wwwAuthHeader)
				httpError(w, "Invalid token", http.StatusUnauthorized)
				return
			}

			log.Debugf("Registry Handler: Token validated, roles=%v, registry_user_uuid=%s", claims.Roles, claims.RegistryUserUUID)

			// Check if this is a RegistryUser token
			if claims.RegistryUserUUID == "" {
				// Not a RegistryUser token - could be a regular User token
				// For now, we'll reject non-RegistryUser tokens for registry access
				// TODO: Later we might allow regular Users with registry permissions
				log.Warnf("Registry Handler: Token does not have RegistryUserUUID, rejecting")
				w.Header().Set("WWW-Authenticate", wwwAuthHeader)
				httpError(w, "Registry access requires RegistryUser token", http.StatusForbidden)
				return
			}

			// Attach claims to request context and continue
			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Basic auth was provided - let it through to the registry server
		// The registry server's BasicAuth wrapper will validate RegistryUser credentials
		if strings.HasPrefix(authHeader, "Basic ") {
			log.Debugf("Registry Handler: Basic auth provided, passing through to registry server for validation")
			next.ServeHTTP(w, r)
			return
		}

		// Unknown auth type
		log.Warnf("Registry Handler: Unknown authorization type: %s", strings.Split(authHeader, " ")[0])
		w.Header().Set("WWW-Authenticate", wwwAuthHeader)
		httpError(w, "Unsupported authorization type", http.StatusUnauthorized)
	})
}

// lookupAndValidateRegistryUser / resolveRegistryUserPermissions:
// izcr-only (OCI registry user support). Hulation does not have a
// registry concept; these functions are intentionally omitted. The
// RegistryUserUUID field on Claims is kept for JSON compat and will
// always be the empty string in hulation-issued tokens.

// for HTTP handlers
func (ware *Authware) AuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unconditional logging to track all HTTP requests
		log.Debugf("HTTP Handler: Received %s %s", r.Method, r.URL.Path)

		// Find the associated gRPC method for this HTTP endpoint
		// We need to check if this endpoint needs authentication
		grpcMethod := ""
		httpPath := r.URL.Path
		httpMethod := r.Method
		foundMatchingMethod := false

		// Find the corresponding gRPC method for this HTTP endpoint
		muMethodMap.RLock()
		for method, info := range methodHTTPMap {
			if info.Path == httpPath && (info.Method == httpMethod || info.Method == "") {
				grpcMethod = method
				foundMatchingMethod = true
				// If we found a matching path but the method doesn't require auth, skip auth checking
				if !info.NeedAuth {
					if ware.debuglevel > 1 {
						log.Debugf("no auth needed for HTTP %s %s (mapped to gRPC %s)", httpMethod, httpPath, grpcMethod)
					}
					next.ServeHTTP(w, r)
					muMethodMap.RUnlock()
					return
				}
				break
			}
		}
		muMethodMap.RUnlock()

		// If we didn't find a matching method, log a warning but continue with auth check as fallback
		if !foundMatchingMethod && ware.debuglevel > 0 {
			log.Warnf("No matching gRPC method found for HTTP %s %s - enforcing auth as fallback", httpMethod, httpPath)
		}

		// Extract the token from the Authorization header or cookie
		var tokenString string
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			// Split the header to get the token part
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
			if tokenString == authHeader {
				http.Error(w, "Invalid token format", http.StatusUnauthorized)
				return
			}
		} else {
			// Try to get token from cookie (set by OAuth callback)
			cookie, err := r.Cookie("izcr_token")
			if err != nil || cookie.Value == "" {
				log.Debugf("HTTP Handler: No Authorization header or izcr_token cookie found")
				http.Error(w, "Authorization header or token cookie missing", http.StatusUnauthorized)
				return
			}
			tokenString = cookie.Value
			log.Debugf("HTTP Handler: Using token from izcr_token cookie")
		}

		// Validate the token
		log.Debugf("HTTP Handler: About to validate token")
		claims, err := ware.validateToken(tokenString)
		if err != nil {
			log.Errorf("HTTP Handler: Token validation failed: %v", err)
			http.Error(w, fmt.Sprintf("Token validation failed: %v", err), http.StatusUnauthorized)
			return
		}

		if claims == nil {
			log.Errorf("HTTP Handler: Claims are nil after validation")
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		log.Debugf("HTTP Handler: Token validated successfully, roles=%v", claims.Roles)

		// Check email validation for HTTP endpoints
		// Skip for: endpoints annotated with skip_email_validation, admin user, internal services, registry users
		if !ShouldSkipEmailValidationForHTTP(httpMethod, httpPath) &&
			claims.Username != "admin" && claims.Subject != "admin" &&
			claims.IDProviderName != "certificate" &&
			claims.RegistryUserUUID == "" &&
			!claims.IsEmailValidated() {
			log.Debugf("HTTP Handler: Email validation required but not present for user %s on %s %s", claims.Username, httpMethod, httpPath)
			http.Error(w, "Email validation required: please verify your email address", http.StatusForbidden)
			return
		}

		// Check limited token restrictions for HTTP endpoints
		if grpcMethod != "" {
			if err := ware.checkLimitedToken(grpcMethod, claims); err != nil {
				log.Debugf("HTTP Handler: Limited token check failed for %s %s: %v", httpMethod, httpPath, err)
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}

		// Check TOTP pending token restrictions for HTTP endpoints
		if grpcMethod != "" {
			if err := ware.checkTotpPendingToken(grpcMethod, claims); err != nil {
				log.Debugf("HTTP Handler: TOTP pending token check failed for %s %s: %v", httpMethod, httpPath, err)
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}

		// Check auth setup for HTTP endpoints
		// Skip for: endpoints annotated with skip_auth_setup_check, admin user, internal services, registry users
		if !ShouldSkipAuthSetupCheckForHTTP(httpMethod, httpPath) &&
			claims.Username != "admin" && claims.Subject != "admin" &&
			claims.IDProviderName != "certificate" &&
			claims.RegistryUserUUID == "" &&
			claims.NeedsAuthSetup() {
			log.Debugf("HTTP Handler: Auth setup required but not complete for user %s on %s %s", claims.Username, httpMethod, httpPath)
			http.Error(w, "Authentication setup required: please set a password or complete login", http.StatusForbidden)
			return
		}

		// Add claims to the request context
		ctx := context.WithValue(r.Context(), ClaimsKey, claims)

		// Serialize claims to JSON for grpc-gateway to pass to gRPC interceptor
		claimsJSON, err := json.Marshal(claims)
		if err != nil {
			log.Errorf("HTTP Handler: failed to serialize claims: %v", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}

		log.Debugf("HTTP Handler: Claims serialized, setting headers")

		// Add claims as HTTP headers so grpc-gateway can convert them to gRPC metadata
		// The gRPC interceptor will recognize these and skip re-authentication
		r.Header.Set("has-auth-claims", "true")
		r.Header.Set("auth-claims-json", string(claimsJSON))

		log.Debugf("HTTP Handler: Headers set, about to check OPA policy for %s %s with roles %v", r.Method, r.URL.Path, claims.Roles)

		// Extract path parameters for permission template resolution
		var requestParams map[string]string
		var requiredPerms []string
		var requireAll bool

		// Find method info to get permission requirements and extract path params
		muMethodMap.RLock()
		for _, info := range methodHTTPMap {
			if pathMatchesTemplate(r.URL.Path, info.Path) && (info.Method == r.Method || info.Method == "") {
				requiredPerms = info.RequiredPermissions
				requireAll = info.RequireAll
				requestParams = ExtractPathParams(r.URL.Path, info.Path)
				log.Debugf("HTTP Handler: Found method info, requiredPerms=%v, pathParams=%v", requiredPerms, requestParams)
				break
			}
		}
		muMethodMap.RUnlock()

		// Check OPA policy with permission-based authorization
		allowed, err := ware.checkPolicyWithPermissions(ctx, r.Method, r.URL.Path, claims,
			requiredPerms, requireAll, requestParams)
		if err != nil {
			if ware.debuglevel > 0 {
				log.Errorf("HTTP Handler: policy check failed for %s %s: %v", r.Method, r.URL.Path, err)
			}
			http.Error(w, "Error evaluating policy", http.StatusInternalServerError)
			return
		}
		if ware.debuglevel > 0 {
			log.Debugf("HTTP Handler: policy check result for %s %s: allowed=%v", r.Method, r.URL.Path, allowed)
		}
		if !allowed {
			if ware.debuglevel > 1 {
				log.Debugf("forbidden for %s %s", r.Method, r.URL.Path)
				log.Debugf("  Claims: %+v", claims)
			}
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// for use with gRPC
func (ware *Authware) AuthInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	log.Debugf("in AuthInterceptor... %s", info.FullMethod)

	// Check for internal certificate-based authentication first
	if internalAuth, ok := ctx.Value("internal_auth").(bool); ok && internalAuth {
		if myriID, ok := ctx.Value("myri_id").(string); ok {
			log.Debugf("AuthInterceptor: Internal certificate authentication detected for myri_id: %s", myriID)

			// Create minimal claims for internal service using certificate authentication
			claims := &Claims{
				RegisteredClaims: jwt.RegisteredClaims{
					Subject: myriID,
				},
				Username:       myriID,
				Roles:          []string{"internal_service"},
				IDProviderName: "certificate", // Indicates certificate-based auth
				Permissions:    []string{"internal_api_access"},
			}

			// Add claims to context for downstream services
			ctx = context.WithValue(ctx, ClaimsKey, claims)

			log.Debugf("AuthInterceptor: Bypassing header auth for internal service, proceeding with certificate auth")
			// Skip header-based authentication for internal services
			return handler(ctx, req)
		}
	} else {
		log.Debugf("AuthInterceptor: No internal auth context found, checking for normal auth headers")
	}

	// Continue with existing header-based authentication logic for external services
	// we need to lookup the associated http method and path for the gRPC method
	// this is because the OPA policy is based on the HTTP method and path
	muMethodMap.RLock()
	httpInfo, ok := methodHTTPMap[info.FullMethod]
	muMethodMap.RUnlock()
	if !ok {
		return nil, fmt.Errorf("grpc not found in map table: %s", info.FullMethod)
	}
	if !httpInfo.NeedAuth {
		if ware.debuglevel > 1 {
			log.Debugf("no auth needed for %s", info.FullMethod)
		}
		return handler(ctx, req)
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing metadata")
	}

	if md == nil {
		return nil, fmt.Errorf("metadata is nil")
	}

	// Check if this request came from the HTTP gateway with valid claims
	if ware.debuglevel > 0 {
		log.Debugf("gRPC Interceptor: checking for auth claims in metadata for method: %s", info.FullMethod)
		log.Debugf("gRPC Interceptor: metadata keys: %v", md)
	}
	if hasAuthClaims, ok := md["has-auth-claims"]; ok && len(hasAuthClaims) > 0 && hasAuthClaims[0] == "true" {
		log.Debugf("Found auth claims in metadata from HTTP gateway for method: %s", info.FullMethod)

		// Extract the serialized claims from metadata
		if serializedClaims, ok := md["auth-claims-json"]; ok && len(serializedClaims) > 0 {
			log.Debugf("Found serialized claims in metadata, deserializing")

			// Deserialize the claims
			var claims Claims
			err := json.Unmarshal([]byte(serializedClaims[0]), &claims)
			if err != nil {
				log.Errorf("Failed to deserialize claims: %v", err)
				return nil, fmt.Errorf("invalid auth claims format")
			}

			log.Debugf("Successfully deserialized claims: %+v", claims)

			// Add claims to the context
			ctx = context.WithValue(ctx, ClaimsKey, &claims)

			// We already verified these claims in the HTTP middleware, so we can proceed
			return handler(ctx, req)
		} else {
			log.Errorf("No auth-claims-json found in metadata even though has-auth-claims is true")
			return nil, fmt.Errorf("missing auth claims data")
		}
	}

	// Normal gRPC authentication flow for direct gRPC calls
	authheader, ok := md["authorization"]
	if !ok {
		return nil, fmt.Errorf("authorization header not found")
	}
	tokenString := authheader[0]

	// Strip "Bearer " prefix if present (same as HTTP handler)
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	// Validate the token
	claims, err2 := ware.validateToken(tokenString)
	if err2 != nil {
		// http.Error(w, "Error on token validation", http.StatusInternalServerError)
		return nil, fmt.Errorf("invalid token: %v", err2)
	}

	if claims == nil {
		// http.Error(w, "Invalid token", http.StatusUnauthorized)
		log.Errorf("gRPC Interceptor: claims are nil")
		return nil, fmt.Errorf("invalid token")
	}

	// Check email validation before proceeding
	if err := ware.checkEmailValidation(info.FullMethod, claims); err != nil {
		log.Debugf("gRPC Interceptor: email validation failed for %s: %v", info.FullMethod, err)
		return nil, fmt.Errorf("email validation required: please verify your email address")
	}

	// Check limited token restrictions before proceeding
	if err := ware.checkLimitedToken(info.FullMethod, claims); err != nil {
		log.Debugf("gRPC Interceptor: limited token check failed for %s: %v", info.FullMethod, err)
		return nil, err
	}

	// Check TOTP pending token restrictions before proceeding
	if err := ware.checkTotpPendingToken(info.FullMethod, claims); err != nil {
		log.Debugf("gRPC Interceptor: TOTP pending token check failed for %s: %v", info.FullMethod, err)
		return nil, err
	}

	// Check auth setup before proceeding
	if err := ware.checkAuthSetup(info.FullMethod, claims); err != nil {
		log.Debugf("gRPC Interceptor: auth setup check failed for %s: %v", info.FullMethod, err)
		return nil, fmt.Errorf("authentication setup required: please set a password or complete login")
	}

	log.Debugf("gRPC Interceptor: About to check OPA policy for method=%s, path=%s, roles=%v", httpInfo.Method, httpInfo.Path, claims.Roles)

	// Extract request parameters for permission template resolution
	requestParams := extractRequestParams(req)
	log.Debugf("gRPC Interceptor: Extracted request params: %v", requestParams)

	// Check OPA policy with permission-based authorization
	allowed, err := ware.checkPolicyWithPermissions(ctx, httpInfo.Method, httpInfo.Path, claims,
		httpInfo.RequiredPermissions, httpInfo.RequireAll, requestParams)
	if err != nil {
		// http.Error(w, "Error evaluating policy", http.StatusInternalServerError)
		log.Errorf("gRPC Interceptor: policy check FAILED for %s: %v", info.FullMethod, err)
		return nil, fmt.Errorf("error evaluating policy: %v", err)
	}

	log.Debugf("gRPC Interceptor: policy check result: allowed=%v for %s", allowed, info.FullMethod)
	if !allowed {
		// http.Error(w, "Forbidden", http.StatusForbidden)
		if ware.debuglevel > 1 {
			log.Debugf("forbidden for %s (err %v)", info.FullMethod, err)
			log.Debugf("  Claims: %+v", claims)
		}
		return nil, fmt.Errorf("forbidden")
	}

	// Add claims to the context before calling the handler
	ctx = context.WithValue(ctx, ClaimsKey, claims)
	return handler(ctx, req)
}

// validateToken tries each validation function until one succeeds or all fail
func (w *Authware) validateToken(tokenString string) (*Claims, error) {
	var lastErr error

	for i, validateFunc := range w.validateTokenFuncs {
		if w.debuglevel > 1 {
			log.Debugf("Trying token validation function %d/%d", i+1, len(w.validateTokenFuncs))
		}

		claims, err := validateFunc(tokenString)
		if err == nil && claims != nil {
			if w.debuglevel > 1 {
				log.Infof("Token validation succeeded with function %d/%d", i+1, len(w.validateTokenFuncs))
			}
			return claims, nil
		}

		if w.debuglevel > 1 {
			log.Debugf("Token validation failed with function %d/%d: %v", i+1, len(w.validateTokenFuncs), err)
		}
		lastErr = err
	}

	// If we get here, all validation functions failed
	if lastErr != nil {
		return nil, fmt.Errorf("all token validation functions failed, last error: %v", lastErr)
	}
	return nil, fmt.Errorf("all token validation functions failed")
}

// NewAuthwareSingle creates an Authware instance with a single token validation function
// This is provided for backward compatibility
func NewAuthwareSingle(strg common.Storage, opapolicy string, checkTokenFunc DecodeTokenFunc, debuglevel int) (ware *Authware, err error) {
	return NewAuthware(strg, opapolicy, []DecodeTokenFunc{checkTokenFunc}, debuglevel)
}

// AddTokenValidator adds an additional token validation function to the Authware instance
func (w *Authware) AddTokenValidator(validateFunc DecodeTokenFunc) {
	if validateFunc != nil {
		w.validateTokenFuncs = append(w.validateTokenFuncs, validateFunc)
		if w.debuglevel > 0 {
			log.Debugf("Added token validator, total count: %d", len(w.validateTokenFuncs))
		}
	}
}
