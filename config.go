package traefik_plugin_ssi

// Config the plugin configuration.
type Config struct {
	IncludeTimeoutSeconds int `json:"includeTimeoutSeconds,omitempty"`
	CacheTTLSeconds       int `json:"cacheTTLSeconds,omitempty"`
	MaxConcurrency        int `json:"maxConcurrency,omitempty"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		IncludeTimeoutSeconds: 1, // 5s → 1s to reduce tail latency
		CacheTTLSeconds:       300,
		MaxConcurrency:        16, // 8 → 16 for better parallelism with 44 includes
	}
}
