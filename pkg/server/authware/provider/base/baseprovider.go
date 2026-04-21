package base

import (
	"context"
	"fmt"

	"github.com/tlalocweb/hulation/log"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"gopkg.in/yaml.v3"
)

// Base provider implementations

type AuthProviderConfig struct {
	Name         string `yaml:"name"`     // this is how we will reference the provider in the API and using myrictl. It must be unique
	ProviderType string `yaml:"provider"` // a well known provider type. Supported types are "keycloak-pw" and "dex"
	// one or more the following keys maybe valid based on your provide type
	// ClientID     string     `yaml:"client_id"`
	// ClientSecret string     `yaml:"client_secret"`
	// RedirectURI  string     `yaml:"redirect_uri"`
	Config yaml.Node `yaml:"config,omitempty"` // Flexible config field that can store arbitrary YAML
}
type CtxKey string

const (CtxKeyJWTFactory CtxKey = "jwtFactory")

// DecodeConfig decodes the Config field into the provided target struct
func (p *AuthProviderConfig) DecodeConfig(target interface{}) error {
	// if p.Config.Kind == yaml. {
	// 	return fmt.Errorf("config field is nil")
	// }

	// // Debug the node
	// log.Debugf("Decoding config node: Kind=%v, Tag=%v, Value=%v", p.Config.Kind, p.Config.Tag, p.Config.Value)

	// // For mapping nodes, we can debug the content more thoroughly
	// if p.Config.Kind == yaml.MappingNode {
	// 	log.Debugf("Config has %d child nodes", len(p.Config.Content))
	// 	for i := 0; i < len(p.Config.Content); i += 2 {
	// 		if i+1 < len(p.Config.Content) {
	// 			key := p.Config.Content[i].Value
	// 			valNode := p.Config.Content[i+1]
	// 			log.Debugf("  Key: %s, Value kind: %v, tag: %v, value: %v",
	// 				key, valNode.Kind, valNode.Tag, valNode.Value)
	// 		}
	// 	}
	// }

	err := p.Config.Decode(target)
	if err != nil {
		// If direct decoding fails, try alternative approach
		log.Debugf("Direct decode failed: %v. Trying alternative approach...", err)

		// Convert to a map first, then marshal and unmarshal
		var rawMap map[string]interface{}
		if err := p.Config.Decode(&rawMap); err == nil {
			dataBytes, err := yaml.Marshal(rawMap)
			if err == nil {
				return yaml.Unmarshal(dataBytes, target)
			}
		}
		return fmt.Errorf("could not decode config: %w", err)
	}

	return nil
}

// BaseProvider implements common functionality for all providers
type BaseProvider struct {
	Config *AuthProviderConfig
}

func (p *BaseProvider) Name() string {
	return p.Config.Name
}

func (p *BaseProvider) Type() string {
	return p.Config.ProviderType
}

// func (p *BaseProvider) ClientID() string {
// 	return p.Config.ClientID
// }

// func (p *BaseProvider) ClientSecret() string {
// 	return p.Config.ClientSecret
// }

// func (p *BaseProvider) RedirectURI() string {
// 	return p.Config.RedirectURI
// }

func (p *BaseProvider) LoginWithSecret(ctx context.Context, req *authspec.LoginWithSecretRequest) (resp *authspec.LoginWithSecretResponse, err error) {
	return nil, fmt.Errorf("not implemented")
}

func (p *BaseProvider) LoginOIDC(ctx context.Context, req *authspec.LoginOIDCRequest) (resp *authspec.LoginOIDCResponse, err error) {
	return nil, fmt.Errorf("not implemented")
}

func (p *BaseProvider) ValidateToken(token string) (user *apiobjects.User, valid bool, err error) {
	return nil, false, fmt.Errorf("not implemented")
}

func (p *BaseProvider) UpdatePassword(ctx context.Context, req *authspec.UpdatePasswordRequest) (resp *authspec.UpdatePasswordResponse, err error) {
	return nil, fmt.Errorf("password updates not supported for this authentication provider")
}
