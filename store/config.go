package store

type StoreConfig struct {
	Type string `yaml:"type,omitempty"`
	Path string `yaml:"path,omitempty"`
}
