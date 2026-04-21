package authware

import (
	"fmt"
	"strings"
	"sync"

	loglib "github.com/tlalocweb/hulation/log"
	izumaauth "github.com/tlalocweb/hulation/protoext/izuma/auth"
	authannotations "github.com/tlalocweb/hulation/pkg/server/authware/proto"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

var log *loglib.TaggedLogger

func init() {
	log = loglib.GetTaggedLogger("AUTHWR", "Auth middleware subsystem")
}

// MethodInfo stores precomputed information about a gRPC method
type MethodInfo struct {
	Method              string
	Path                string
	NeedAuth            bool
	SkipEmailValidation bool
	SkipAuthSetupCheck  bool
	SkipTotpCheck       bool
	// Permission-based RBAC fields
	RequiredPermissions []string // Permission strings from proto annotation
	RequireAll          bool     // If true, all permissions required; if false, any one is sufficient
}

var methodHTTPMap = make(map[string]MethodInfo)
var muMethodMap sync.RWMutex

var precomputedOk bool

// grpc uses these two way to name functions:
// example:  izcr.v1.status.StatusService.GetStatus
// and: /izcr.v1.status.StatusService/GetStatus
// this transforms the first into the second, which is the one used by
// grpc over HTTP2
func transformToSlashFormat(grpcMethod string) string {
	if len(grpcMethod) == 0 {
		return grpcMethod
	}

	var builder strings.Builder
	builder.WriteByte('/')

	lastDotIndex := strings.LastIndex(grpcMethod, ".")
	if lastDotIndex == -1 {
		builder.WriteString(grpcMethod)
		return builder.String()
	}

	builder.WriteString(grpcMethod[:lastDotIndex])
	builder.WriteByte('/')
	builder.WriteString(grpcMethod[lastDotIndex+1:])

	return builder.String()
}

func precomputeHTTPMethods() {
	if !precomputedOk {
		protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
			for i := 0; i < fd.Services().Len(); i++ {
				service := fd.Services().Get(i)
				for j := 0; j < service.Methods().Len(); j++ {
					method := service.Methods().Get(j)
					fn := method.FullName()
					fn2 := transformToSlashFormat(string(fn))
					// only worry about izcr and agentctl RPCs
					if strings.Contains(string(fn), "izcr") || strings.Contains(string(fn), "agentctl") {
						priv, err := getPrivileged(method)
						if err != nil {
							log.Debugf("(this is likely expected) Error extracting privileged for %s: %v", fn, err)
						} else {
							if priv {
								log.Debugf("Method %s is privileged", fn)
							}
						}
						skipEmailValidation, err := getSkipEmailValidation(method)
						if err != nil {
							log.Debugf("(this is likely expected) Error extracting skip_email_validation for %s: %v", fn, err)
						}
						skipAuthSetupCheck, err := getSkipAuthSetupCheck(method)
						if err != nil {
							log.Debugf("(this is likely expected) Error extracting skip_auth_setup_check for %s: %v", fn, err)
						}
						skipTotpCheck, err := getSkipTotpCheck(method)
						if err != nil {
							log.Debugf("(this is likely expected) Error extracting skip_totp_check for %s: %v", fn, err)
						}
						httpMethod, httpPath, err := getHTTPMethodAndPath(method)
						if err != nil {
							log.Debugf("(this is likely expected) Error extracting HTTP method and path for %s: %v", fn, err)
						}
						// Extract permission requirements
						requiredPerms, requireAll, err := getRequiredPermissions(method)
						if err != nil {
							log.Debugf("(this is likely expected) Error extracting permissions for %s: %v", fn, err)
						}
						muMethodMap.Lock()
						methodHTTPMap[string(fn2)] = MethodInfo{
							Method:              httpMethod,
							Path:                httpPath,
							NeedAuth:            priv,
							SkipEmailValidation: skipEmailValidation,
							SkipAuthSetupCheck:  skipAuthSetupCheck,
							SkipTotpCheck:       skipTotpCheck,
							RequiredPermissions: requiredPerms,
							RequireAll:          requireAll,
						}
						muMethodMap.Unlock()
						if len(requiredPerms) > 0 {
							log.Debugf("Precomputed permissions for %s: %v (requireAll=%v)", fn2, requiredPerms, requireAll)
						}
						log.Debugf("Precomputed HTTP method and path for %s: %s %s (skipEmailValidation=%v, skipAuthSetupCheck=%v)", fn2, httpMethod, httpPath, skipEmailValidation, skipAuthSetupCheck)
					}
				}
			}
			return true
		})
		precomputedOk = true
	}
}

func getHTTPMethodAndPath(methodDesc protoreflect.MethodDescriptor) (string, string, error) {
	options := methodDesc.Options().(*descriptorpb.MethodOptions)
	ext := proto.GetExtension(options, annotations.E_Http)
	// if err != nil {
	// 	return "", "", err
	// }

	httpRule, ok := ext.(*annotations.HttpRule)
	if !ok {
		return "", "", fmt.Errorf("no HTTP rule found")
	}
	if httpRule == nil {
		return "", "", fmt.Errorf("no HTTP rule found")
	}
	if httpRule.Pattern == nil {
		return "", "", fmt.Errorf("no HTTP pattern found")
	}
	switch pattern := httpRule.Pattern.(type) {
	case *annotations.HttpRule_Get:
		return "GET", pattern.Get, nil
	case *annotations.HttpRule_Post:
		return "POST", pattern.Post, nil
	case *annotations.HttpRule_Put:
		return "PUT", pattern.Put, nil
	case *annotations.HttpRule_Delete:
		return "DELETE", pattern.Delete, nil
	case *annotations.HttpRule_Patch:
		return "PATCH", pattern.Patch, nil
	case *annotations.HttpRule_Custom:
		return pattern.Custom.Kind, pattern.Custom.Path, nil
	default:
		return "", "", fmt.Errorf("unsupported HTTP method")
	}
}

func getPrivileged(methodDesc protoreflect.MethodDescriptor) (bool, error) {
	options := methodDesc.Options().(*descriptorpb.MethodOptions)
	ext := proto.GetExtension(options, authannotations.E_Noauth)
	if ext != nil {
		noauthok, ok := ext.(bool)
		if !ok {
			return true, fmt.Errorf("invalid extension type")
		}
		return !noauthok, nil
	}
	// by default all APIs require auth
	return true, nil
}

// getSkipEmailValidation returns true if the method has skip_email_validation = true annotation
func getSkipEmailValidation(methodDesc protoreflect.MethodDescriptor) (bool, error) {
	options := methodDesc.Options().(*descriptorpb.MethodOptions)
	ext := proto.GetExtension(options, authannotations.E_SkipEmailValidation)
	if ext != nil {
		skipEmail, ok := ext.(bool)
		if !ok {
			return false, fmt.Errorf("invalid extension type")
		}
		return skipEmail, nil
	}
	// by default, email validation is required
	return false, nil
}

// ShouldSkipEmailValidation checks if a gRPC method should skip email validation
// This is used by the middleware to determine if email validation should be enforced
func ShouldSkipEmailValidation(grpcMethod string) bool {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()
	if info, ok := methodHTTPMap[grpcMethod]; ok {
		return info.SkipEmailValidation
	}
	// If method not found, require email validation by default
	return false
}

// RequiresAuth checks if a gRPC method requires authentication
func RequiresAuth(grpcMethod string) bool {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()
	if info, ok := methodHTTPMap[grpcMethod]; ok {
		return info.NeedAuth
	}
	// If method not found, require auth by default
	return true
}

// ShouldSkipEmailValidationForHTTP checks if an HTTP endpoint should skip email validation
// by looking up the corresponding gRPC method from the HTTP method and path
func ShouldSkipEmailValidationForHTTP(httpMethod, httpPath string) bool {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()

	// Look through all methods to find one matching this HTTP method/path
	for _, info := range methodHTTPMap {
		if info.Method == httpMethod && info.Path == httpPath {
			return info.SkipEmailValidation
		}
	}
	// If not found, require email validation by default
	return false
}

// getSkipAuthSetupCheck returns true if the method has skip_auth_setup_check = true annotation
func getSkipAuthSetupCheck(methodDesc protoreflect.MethodDescriptor) (bool, error) {
	options := methodDesc.Options().(*descriptorpb.MethodOptions)
	ext := proto.GetExtension(options, authannotations.E_SkipAuthSetupCheck)
	if ext != nil {
		skipAuthSetup, ok := ext.(bool)
		if !ok {
			return false, fmt.Errorf("invalid extension type")
		}
		return skipAuthSetup, nil
	}
	// by default, auth setup check is required
	return false, nil
}

// ShouldSkipAuthSetupCheck checks if a gRPC method should skip auth setup check
// This is used by the middleware to determine if auth setup validation should be enforced
func ShouldSkipAuthSetupCheck(grpcMethod string) bool {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()
	if info, ok := methodHTTPMap[grpcMethod]; ok {
		return info.SkipAuthSetupCheck
	}
	// If method not found, require auth setup check by default
	return false
}

// ShouldSkipAuthSetupCheckForHTTP checks if an HTTP endpoint should skip auth setup check
// by looking up the corresponding gRPC method from the HTTP method and path
func ShouldSkipAuthSetupCheckForHTTP(httpMethod, httpPath string) bool {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()

	// Look through all methods to find one matching this HTTP method/path
	for _, info := range methodHTTPMap {
		if info.Method == httpMethod && info.Path == httpPath {
			return info.SkipAuthSetupCheck
		}
	}
	// If not found, require auth setup check by default
	return false
}

// getSkipTotpCheck returns true if the method has skip_totp_check = true annotation
func getSkipTotpCheck(methodDesc protoreflect.MethodDescriptor) (bool, error) {
	options := methodDesc.Options().(*descriptorpb.MethodOptions)
	ext := proto.GetExtension(options, authannotations.E_SkipTotpCheck)
	if ext != nil {
		skipTotp, ok := ext.(bool)
		if !ok {
			return false, fmt.Errorf("invalid extension type")
		}
		return skipTotp, nil
	}
	// by default, TOTP check is required
	return false, nil
}

// ShouldSkipTotpCheck checks if a gRPC method should skip TOTP check
// This is used by the middleware to determine if TOTP validation should be enforced
func ShouldSkipTotpCheck(grpcMethod string) bool {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()
	if info, ok := methodHTTPMap[grpcMethod]; ok {
		return info.SkipTotpCheck
	}
	// If method not found, require TOTP check by default
	return false
}

// ShouldSkipTotpCheckForHTTP checks if an HTTP endpoint should skip TOTP check
// by looking up the corresponding gRPC method from the HTTP method and path
func ShouldSkipTotpCheckForHTTP(httpMethod, httpPath string) bool {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()

	// Look through all methods to find one matching this HTTP method/path
	for _, info := range methodHTTPMap {
		if info.Method == httpMethod && info.Path == httpPath {
			return info.SkipTotpCheck
		}
	}
	// If not found, require TOTP check by default
	return false
}

// getRequiredPermissions extracts permission requirements from a method's proto annotations
func getRequiredPermissions(methodDesc protoreflect.MethodDescriptor) ([]string, bool, error) {
	options := methodDesc.Options().(*descriptorpb.MethodOptions)
	ext := proto.GetExtension(options, izumaauth.E_Permission)
	if ext == nil {
		return nil, false, nil
	}

	permReq, ok := ext.(*izumaauth.PermissionRequirement)
	if !ok || permReq == nil {
		return nil, false, fmt.Errorf("invalid permission extension type")
	}

	return permReq.Needs, permReq.RequireAll, nil
}

// GetMethodPermissions returns the permission requirements for a gRPC method
func GetMethodPermissions(grpcMethod string) ([]string, bool) {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()
	if info, ok := methodHTTPMap[grpcMethod]; ok {
		return info.RequiredPermissions, info.RequireAll
	}
	return nil, false
}

// GetMethodInfo returns the full method info for a gRPC method
func GetMethodInfo(grpcMethod string) (MethodInfo, bool) {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()
	info, ok := methodHTTPMap[grpcMethod]
	return info, ok
}

// GetMethodInfoByHTTP returns the method info for an HTTP method/path combination
func GetMethodInfoByHTTP(httpMethod, httpPath string) (MethodInfo, bool) {
	precomputeHTTPMethods()
	muMethodMap.RLock()
	defer muMethodMap.RUnlock()

	for _, info := range methodHTTPMap {
		if info.Method == httpMethod && pathMatchesTemplate(httpPath, info.Path) {
			return info, true
		}
	}
	return MethodInfo{}, false
}

// pathMatchesTemplate checks if an actual HTTP path matches a template path
// e.g., "/api/v1/tenants/abc123/projects" matches "/api/v1/tenants/{tenant_uuid}/projects"
func pathMatchesTemplate(actualPath, templatePath string) bool {
	// Simple exact match first
	if actualPath == templatePath {
		return true
	}

	// Split paths into segments
	actualParts := strings.Split(strings.Trim(actualPath, "/"), "/")
	templateParts := strings.Split(strings.Trim(templatePath, "/"), "/")

	if len(actualParts) != len(templateParts) {
		return false
	}

	for i := range actualParts {
		// Template placeholder matches any value
		if strings.HasPrefix(templateParts[i], "{") && strings.HasSuffix(templateParts[i], "}") {
			continue
		}
		// Must be exact match
		if actualParts[i] != templateParts[i] {
			return false
		}
	}

	return true
}

// ExtractPathParams extracts parameter values from an actual path given a template
// e.g., extractPathParams("/api/v1/tenants/abc123/projects/xyz789", "/api/v1/tenants/{tenant_uuid}/projects/{project_uuid}")
// returns {"tenant_uuid": "abc123", "project_uuid": "xyz789"}
func ExtractPathParams(actualPath, templatePath string) map[string]string {
	params := make(map[string]string)

	actualParts := strings.Split(strings.Trim(actualPath, "/"), "/")
	templateParts := strings.Split(strings.Trim(templatePath, "/"), "/")

	if len(actualParts) != len(templateParts) {
		return params
	}

	for i := range actualParts {
		if strings.HasPrefix(templateParts[i], "{") && strings.HasSuffix(templateParts[i], "}") {
			// Extract parameter name (remove { and })
			paramName := templateParts[i][1 : len(templateParts[i])-1]
			params[paramName] = actualParts[i]
		}
	}

	return params
}
