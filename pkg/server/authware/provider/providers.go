package base

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tlalocweb/hulation/log"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	baseprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/base"

	// dexprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/dex"
	internalprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/internal"
	// keyclockprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/keycloak"
	oidcprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/oidc"
)

// var log *loglib.TaggedLogger

// func init() {
// 	log = loglib.GetTaggedLogger("AUTHMG", "Auth Provider Manager")
// }

// AuthProvider defines the interface that all authentication providers must implement
type AuthProvider interface {
	// Initialize sets up the provider with any needed connections or resources
	Initialize(ctx context.Context) error

	// Shutdown cleanly releases any resources used by the provider
	Shutdown(ctx context.Context) error

	// Name returns the unique identifier for this provider
	Name() string

	// Type returns the provider type (e.g. "keycloak-pw", "dex")
	Type() string

	// IsHealthy returns whether the provider is currently operational
	IsHealthy(ctx context.Context) bool

	// ValidateToken validates a token - returns the user if valid
	ValidateToken(token string) (user *apiobjects.User, valid bool, err error)

	// LoginOIDC logs in with an OIDC provider
	LoginOIDC(ctx context.Context, req *authspec.LoginOIDCRequest) (resp *authspec.LoginOIDCResponse, err error)
}

// ProviderManager handles multiple authentication providers and their lifecycle
type ProviderManager struct {
	providers map[string]AuthProvider
	mutex     sync.RWMutex
}

var providerManager *ProviderManager

// GetProviderManager creates a new ProviderManager instance or returns the existing one
func GetProviderManager() *ProviderManager {
	if providerManager == nil {
		providers := make(map[string]AuthProvider)
		providerManager = &ProviderManager{
			providers: providers,
		}
	}
	return providerManager
}

// func (pm *ProviderManager) SetupAllProviders(ctx context.Context, provs []*baseprovider.AuthProviderConfig) (errs map[string]error) {
// 	errs = make(map[string]error)
// 	for _, prov := range provs {
// 		provider, err := CreateProvider(prov)
// 		if err != nil {
// 			errs[prov.Name] = err
// 			continue
// 		}
// 		// check to make sure provider has a name
// 		if provider.Name() == "" {
// 			errs[prov.Name] = fmt.Errorf("auth provider '%s' has no name", prov.Name)
// 			continue
// 		}
// 		// check to make sure there is not another provider of the same name
// 		if _, exists := pm.providers[provider.Name()]; exists {
// 			errs[prov.Name] = fmt.Errorf("auth provider '%s' already registered", prov.Name)
// 			continue
// 		}
// 		pm.providers[provider.Name()] = provider
// 	}
// 	return errs
// }

// RegisterProvider adds a new provider to the manager
func (pm *ProviderManager) RegisterProvider(provider AuthProvider) error {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	name := provider.Name()
	if name == "" {
		return errors.New("auth provider must have a name")
	}

	if _, exists := pm.providers[name]; exists {
		return fmt.Errorf("auth provider with name '%s' already registered", name)
	}

	pm.providers[name] = provider
	return nil
}

// GetProvider retrieves a provider by name
func (pm *ProviderManager) GetProvider(name string) (AuthProvider, error) {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	provider, exists := pm.providers[name]
	if !exists {
		return nil, fmt.Errorf("auth provider '%s' not found", name)
	}

	return provider, nil
}

func (pm *ProviderManager) GetDefaultProvider() (AuthProvider, error) {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	for _, provider := range pm.providers {
		return provider, nil
	}
	return nil, fmt.Errorf("no default auth provider found")
}

// GetProviders returns all registered providers
func (pm *ProviderManager) GetProviders() []AuthProvider {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	providers := make([]AuthProvider, 0, len(pm.providers))
	for _, provider := range pm.providers {
		providers = append(providers, provider)
	}

	return providers
}

// InitializeAll initializes all registered providers
func (pm *ProviderManager) InitializeAll(ctx context.Context) error {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	for name, provider := range pm.providers {
		if err := provider.Initialize(ctx); err != nil {
			return fmt.Errorf("failed to initialize auth provider '%s': %w", name, err)
		}
	}

	return nil
}

// ShutdownAll shuts down all registered providers
func (pm *ProviderManager) ShutdownAll(ctx context.Context) error {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	var errs []error
	for name, provider := range pm.providers {
		if err := provider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown auth provider '%s': %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors shutting down providers: %v", errs)
	}

	return nil
}

// RemoveProvider unregisters a provider by name
func (pm *ProviderManager) RemoveProvider(ctx context.Context, name string) error {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	provider, exists := pm.providers[name]
	if !exists {
		return fmt.Errorf("auth provider '%s' not found", name)
	}

	// First shut down the provider
	if err := provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown auth provider '%s': %w", name, err)
	}

	// Remove it from the map
	delete(pm.providers, name)
	return nil
}

// // LoadFromConfig loads providers based on the application configuration
// func (pm *ProviderManager) LoadFromConfig(ctx context.Context, cfg *config.Config) (errs []error) {
// 	pm.mutex.Lock()
// 	defer pm.mutex.Unlock()

// 	// Clear any existing providers
// 	for name, provider := range pm.providers {
// 		if err := provider.Shutdown(ctx); err != nil {
// 			errs = append(errs, fmt.Errorf("failed to shutdown existing auth provider '%s': %w", name, err))
// 			continue
// 		}
// 	}

// 	pm.providers = make(map[string]AuthProvider)

// 	// Create providers based on configuration
// 	for _, providerCfg := range cfg.AuthProviders {
// 		if pm.providers[providerCfg.Name] != nil {
// 			log.Errorf("auth provider '%s' already registered", providerCfg.Name)
// 			continue
// 		}
// 		provider, err := CreateProvider(providerCfg)
// 		if err != nil {
// 			log.Errorf("failed to create auth provider '%s': %v", providerCfg.Name, err)
// 			errs = append(errs, fmt.Errorf("failed to create auth provider '%s': %w", providerCfg.Name, err))
// 			continue
// 		}

// 		pm.providers[provider.Name()] = provider
// 	}

// 	return errs
// }

// CreateProvider instantiates a new auth provider based on the configuration
func CreateProvider(cfg *baseprovider.AuthProviderConfig) (AuthProvider, error) {
	switch cfg.ProviderType {
	case "internal":
		return internalprovider.NewIzcrProvider(cfg)
	case "oidc":
		return oidcprovider.NewOIDCProvider(cfg)
	// TODO: Uncomment when providers are fully implemented
	// case "keycloak-pw":
	// 	return keyclockprovider.NewKeycloakProvider(cfg)
	// case "dex":
	// 	return dexprovider.NewDexProvider(cfg)
	default:
		return nil, fmt.Errorf("unsupported auth provider type: %s", cfg.ProviderType)
	}
}

func (pm *ProviderManager) CreateAndRegisterProvdiders(providerconfigs []*baseprovider.AuthProviderConfig) (errs map[string]error) {
	errs = make(map[string]error)
	for _, providerconfig := range providerconfigs {
		provider, err := CreateProvider(providerconfig)
		if err != nil {
			log.Errorf("failed to create auth provider '%s': %v", providerconfig.Name, err)
			errs[providerconfig.Name] = err
			continue
		}
		// make the provider has a name
		if provider.Name() == "" {
			errs[providerconfig.Name] = fmt.Errorf("auth provider '%s' has no name", providerconfig.Name)
			continue
		}
		// check to make sure there is not another provider of the same name
		if _, exists := pm.providers[provider.Name()]; exists {
			errs[providerconfig.Name] = fmt.Errorf("auth provider '%s' already registered", providerconfig.Name)
			continue
		}
		pm.providers[provider.Name()] = provider
		log.Infof("auth provider '%s' registered", provider.Name())
	}
	return errs
}

func (pm *ProviderManager) ValidateToken(token string) (user *apiobjects.User, valid bool, err error) {
	for _, provider := range pm.providers {
		user, valid, err = provider.ValidateToken(token)
		if err != nil {
			return nil, false, err
		}
		if valid {
			return user, true, nil
		}
	}
	return nil, false, nil
}
