package gateway

import (
	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/provider"
	"github.com/luxueq/aigateway/internal/provider/anthropic"
	"github.com/luxueq/aigateway/internal/provider/openai"
)

// providerFactories maps a provider name to the constructor that builds its
// adapter. Adding a new provider only requires implementing provider.Provider
// and registering its constructor here — no changes to Proxy wiring.
var providerFactories = map[string]func(*config.ProviderConfig) provider.Provider{
	"openai": func(cfg *config.ProviderConfig) provider.Provider {
		return openai.NewOpenAIProvider(cfg)
	},
	"anthropic": func(cfg *config.ProviderConfig) provider.Provider {
		return anthropic.NewAnthropicProvider(cfg)
	},
}
