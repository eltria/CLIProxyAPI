// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing TOML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"syscall"

	"github.com/pelletier/go-toml/v2"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

const (
	DefaultPanelGitHubRepository = "https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
	DefaultPprofAddr             = "127.0.0.1:8316"
)

// Config represents the application's configuration, loaded from a TOML file.
type Config struct {
	SDKConfig
	// Host is the network host/interface on which the API server will bind.
	// Default is empty ("") to bind all interfaces (IPv4 + IPv6). Use "127.0.0.1" or "localhost" for local-only access.
	Host string `toml:"host" json:"-"`
	// Port is the network port on which the API server will listen.
	Port int `toml:"port" json:"-"`

	// TLS config controls HTTPS server settings.
	TLS TLSConfig `toml:"tls" json:"tls"`

	// RemoteManagement nests management-related options under 'remote-management'.
	RemoteManagement RemoteManagement `toml:"remote-management" json:"-"`

	// AuthDir is the directory where authentication token files are stored.
	AuthDir string `toml:"auth-dir" json:"-"`

	// Debug enables or disables debug-level logging and other debug features.
	Debug bool `toml:"debug" json:"debug"`

	// Pprof config controls the optional pprof HTTP debug server.
	Pprof PprofConfig `toml:"pprof" json:"pprof"`

	// CommercialMode disables high-overhead HTTP middleware features to minimize per-request memory usage.
	CommercialMode bool `toml:"commercial-mode" json:"commercial-mode"`

	// LoggingToFile controls whether application logs are written to rotating files or stdout.
	LoggingToFile bool `toml:"logging-to-file" json:"logging-to-file"`

	// LogsMaxTotalSizeMB limits the total size (in MB) of log files under the logs directory.
	// When exceeded, the oldest log files are deleted until within the limit. Set to 0 to disable.
	LogsMaxTotalSizeMB int `toml:"logs-max-total-size-mb" json:"logs-max-total-size-mb"`

	// ErrorLogsMaxFiles limits the number of error log files retained when request logging is disabled.
	// When exceeded, the oldest error log files are deleted. Default is 10. Set to 0 to disable cleanup.
	ErrorLogsMaxFiles int `toml:"error-logs-max-files" json:"error-logs-max-files"`

	// UsageStatisticsEnabled toggles in-memory usage aggregation; when false, usage data is discarded.
	UsageStatisticsEnabled bool `toml:"usage-statistics-enabled" json:"usage-statistics-enabled"`

	// DisableCooling disables quota cooldown scheduling when true.
	DisableCooling bool `toml:"disable-cooling" json:"disable-cooling"`

	// AuthAutoRefreshWorkers overrides the size of the core auth auto-refresh worker pool.
	// When <= 0, the default worker count is used.
	AuthAutoRefreshWorkers int `toml:"auth-auto-refresh-workers" json:"auth-auto-refresh-workers"`

	// RequestRetry defines the retry times when the request failed.
	RequestRetry int `toml:"request-retry" json:"request-retry"`
	// MaxRetryCredentials defines the maximum number of credentials to try for a failed request.
	// Set to 0 or a negative value to keep trying all available credentials (legacy behavior).
	MaxRetryCredentials int `toml:"max-retry-credentials" json:"max-retry-credentials"`
	// MaxRetryInterval defines the maximum wait time in seconds before retrying a cooled-down credential.
	MaxRetryInterval int `toml:"max-retry-interval" json:"max-retry-interval"`

	// QuotaExceeded defines the behavior when a quota is exceeded.
	QuotaExceeded QuotaExceeded `toml:"quota-exceeded" json:"quota-exceeded"`

	// Routing controls credential selection behavior.
	Routing RoutingConfig `toml:"routing" json:"routing"`

	// WebsocketAuth enables or disables authentication for the WebSocket API.
	WebsocketAuth bool `toml:"ws-auth" json:"ws-auth"`

	// AntigravitySignatureCacheEnabled controls whether signature cache validation is enabled for thinking blocks.
	// When true (default), cached signatures are preferred and validated.
	// When false, client signatures are used directly after normalization (bypass mode).
	AntigravitySignatureCacheEnabled *bool `toml:"antigravity-signature-cache-enabled,omitempty" json:"antigravity-signature-cache-enabled,omitempty"`

	AntigravitySignatureBypassStrict *bool `toml:"antigravity-signature-bypass-strict,omitempty" json:"antigravity-signature-bypass-strict,omitempty"`

	// GeminiKey defines Gemini API key configurations with optional routing overrides.
	GeminiKey []GeminiKey `toml:"gemini-api-key" json:"gemini-api-key"`

	// Codex defines a list of Codex API key configurations as specified in the YAML configuration file.
	CodexKey []CodexKey `toml:"codex-api-key" json:"codex-api-key"`

	// CodexHeaderDefaults configures fallback headers for Codex OAuth model requests.
	// These are used only when the client does not send its own headers.
	CodexHeaderDefaults CodexHeaderDefaults `toml:"codex-header-defaults" json:"codex-header-defaults"`

	// ClaudeKey defines a list of Claude API key configurations as specified in the YAML configuration file.
	ClaudeKey []ClaudeKey `toml:"claude-api-key" json:"claude-api-key"`

	// ClaudeHeaderDefaults configures default header values for Claude API requests.
	// These are used as fallbacks when the client does not send its own headers.
	ClaudeHeaderDefaults ClaudeHeaderDefaults `toml:"claude-header-defaults" json:"claude-header-defaults"`

	// OpenAICompatibility defines OpenAI API compatibility configurations for external providers.
	OpenAICompatibility []OpenAICompatibility `toml:"openai-compatibility" json:"openai-compatibility"`

	// VertexCompatAPIKey defines Vertex AI-compatible API key configurations for third-party providers.
	// Used for services that use Vertex AI-style paths but with simple API key authentication.
	VertexCompatAPIKey []VertexCompatKey `toml:"vertex-api-key" json:"vertex-api-key"`

	// AmpCode contains Amp CLI upstream configuration, management restrictions, and model mappings.
	AmpCode AmpCode `toml:"ampcode" json:"ampcode"`

	// OAuthExcludedModels defines per-provider global model exclusions applied to OAuth/file-backed auth entries.
	OAuthExcludedModels map[string][]string `toml:"oauth-excluded-models,omitempty" json:"oauth-excluded-models,omitempty"`

	// OAuthModelAlias defines global model name aliases for OAuth/file-backed auth channels.
	// These aliases affect both model listing and model routing for supported channels:
	// gemini-cli, vertex, aistudio, antigravity, claude, codex, kimi.
	//
	// NOTE: This does not apply to existing per-credential model alias features under:
	// gemini-api-key, codex-api-key, claude-api-key, openai-compatibility, vertex-api-key, and ampcode.
	OAuthModelAlias map[string][]OAuthModelAlias `toml:"oauth-model-alias,omitempty" json:"oauth-model-alias,omitempty"`

	// Payload defines default and override rules for provider payload parameters.
	Payload PayloadConfig `toml:"payload" json:"payload"`

	legacyMigrationPending bool `toml:"-" json:"-"`
}

// ClaudeHeaderDefaults configures default header values injected into Claude API requests.
// In legacy mode, UserAgent/PackageVersion/RuntimeVersion/Timeout act as fallbacks when
// the client omits them, while OS/Arch remain runtime-derived. When stabilized device
// profiles are enabled, OS/Arch become the pinned platform baseline, while
// UserAgent/PackageVersion/RuntimeVersion seed the upgradeable software fingerprint.
type ClaudeHeaderDefaults struct {
	UserAgent              string `toml:"user-agent" json:"user-agent"`
	PackageVersion         string `toml:"package-version" json:"package-version"`
	RuntimeVersion         string `toml:"runtime-version" json:"runtime-version"`
	OS                     string `toml:"os" json:"os"`
	Arch                   string `toml:"arch" json:"arch"`
	Timeout                string `toml:"timeout" json:"timeout"`
	StabilizeDeviceProfile *bool  `toml:"stabilize-device-profile,omitempty" json:"stabilize-device-profile,omitempty"`
}

// CodexHeaderDefaults configures fallback header values injected into Codex
// model requests for OAuth/file-backed auth when the client omits them.
// UserAgent applies to HTTP and websocket requests; BetaFeatures only applies to websockets.
type CodexHeaderDefaults struct {
	UserAgent    string `toml:"user-agent" json:"user-agent"`
	BetaFeatures string `toml:"beta-features" json:"beta-features"`
}

// TLSConfig holds HTTPS server settings.
type TLSConfig struct {
	// Enable toggles HTTPS server mode.
	Enable bool `toml:"enable" json:"enable"`
	// Cert is the path to the TLS certificate file.
	Cert string `toml:"cert" json:"cert"`
	// Key is the path to the TLS private key file.
	Key string `toml:"key" json:"key"`
}

// PprofConfig holds pprof HTTP server settings.
type PprofConfig struct {
	// Enable toggles the pprof HTTP debug server.
	Enable bool `toml:"enable" json:"enable"`
	// Addr is the host:port address for the pprof HTTP server.
	Addr string `toml:"addr" json:"addr"`
}

// RemoteManagement holds management API configuration under 'remote-management'.
type RemoteManagement struct {
	// AllowRemote toggles remote (non-localhost) access to management API.
	AllowRemote bool `toml:"allow-remote"`
	// SecretKey is the management key (plaintext or bcrypt hashed). YAML key intentionally 'secret-key'.
	SecretKey string `toml:"secret-key"`
	// DisableControlPanel skips serving and syncing the bundled management UI when true.
	DisableControlPanel bool `toml:"disable-control-panel"`
	// DisableAutoUpdatePanel disables automatic periodic background updates of the management panel asset from GitHub.
	// When false (the default), the background updater remains enabled; when true, the panel is only downloaded on first access if missing.
	DisableAutoUpdatePanel bool `toml:"disable-auto-update-panel"`
	// PanelGitHubRepository overrides the GitHub repository used to fetch the management panel asset.
	// Accepts either a repository URL (https://github.com/org/repo) or an API releases endpoint.
	PanelGitHubRepository string `toml:"panel-github-repository"`

	// Disable forces the entire management API off regardless of secret-key,
	// MANAGEMENT_PASSWORD env, or -password flag. Hot-reloadable via config.toml.
	Disable bool `toml:"disable"`
}

// QuotaExceeded defines the behavior when API quota limits are exceeded.
// It provides configuration options for automatic failover mechanisms.
type QuotaExceeded struct {
	// SwitchProject indicates whether to automatically switch to another project when a quota is exceeded.
	SwitchProject bool `toml:"switch-project" json:"switch-project"`

	// SwitchPreviewModel indicates whether to automatically switch to a preview model when a quota is exceeded.
	SwitchPreviewModel bool `toml:"switch-preview-model" json:"switch-preview-model"`

	// AntigravityCredits indicates whether to retry Antigravity quota_exhausted 429s once
	// on the same credential with enabledCreditTypes=["GOOGLE_ONE_AI"].
	AntigravityCredits bool `toml:"antigravity-credits" json:"antigravity-credits"`
}

// RoutingConfig configures how credentials are selected for requests.
type RoutingConfig struct {
	// Strategy selects the credential selection strategy.
	// Supported values: "round-robin" (default), "fill-first".
	Strategy string `toml:"strategy,omitempty" json:"strategy,omitempty"`

	// ClaudeCodeSessionAffinity enables session-sticky routing for Claude Code clients.
	// When enabled, requests with the same session ID (extracted from metadata.user_id)
	// are routed to the same auth credential when available.
	// Deprecated: Use SessionAffinity instead for universal session support.
	ClaudeCodeSessionAffinity bool `toml:"claude-code-session-affinity,omitempty" json:"claude-code-session-affinity,omitempty"`

	// SessionAffinity enables universal session-sticky routing for all clients.
	// Session IDs are extracted from multiple sources:
	// X-Session-ID header, Idempotency-Key, metadata.user_id, conversation_id, or message hash.
	// Automatic failover is always enabled when bound auth becomes unavailable.
	SessionAffinity bool `toml:"session-affinity,omitempty" json:"session-affinity,omitempty"`

	// SessionAffinityTTL specifies how long session-to-auth bindings are retained.
	// Default: 1h. Accepts duration strings like "30m", "1h", "2h30m".
	SessionAffinityTTL string `toml:"session-affinity-ttl,omitempty" json:"session-affinity-ttl,omitempty"`
}

// OAuthModelAlias defines a model ID alias for a specific channel.
// It maps the upstream model name (Name) to the client-visible alias (Alias).
// When Fork is true, the alias is added as an additional model in listings while
// keeping the original model ID available.
type OAuthModelAlias struct {
	Name  string `toml:"name" json:"name"`
	Alias string `toml:"alias" json:"alias"`
	Fork  bool   `toml:"fork,omitempty" json:"fork,omitempty"`
}

// AmpModelMapping defines a model name mapping for Amp CLI requests.
// When Amp requests a model that isn't available locally, this mapping
// allows routing to an alternative model that IS available.
type AmpModelMapping struct {
	// From is the model name that Amp CLI requests (e.g., "claude-opus-4.5").
	From string `toml:"from" json:"from"`

	// To is the target model name to route to (e.g., "claude-sonnet-4").
	// The target model must have available providers in the registry.
	To string `toml:"to" json:"to"`

	// Regex indicates whether the 'from' field should be interpreted as a regular
	// expression for matching model names. When true, this mapping is evaluated
	// after exact matches and in the order provided. Defaults to false (exact match).
	Regex bool `toml:"regex,omitempty" json:"regex,omitempty"`
}

// AmpCode groups Amp CLI integration settings including upstream routing,
// optional overrides, management route restrictions, and model fallback mappings.
type AmpCode struct {
	// UpstreamURL defines the upstream Amp control plane used for non-provider calls.
	UpstreamURL string `toml:"upstream-url" json:"upstream-url"`

	// UpstreamAPIKey optionally overrides the Authorization header when proxying Amp upstream calls.
	UpstreamAPIKey string `toml:"upstream-api-key" json:"upstream-api-key"`

	// UpstreamAPIKeys maps client API keys (from top-level api-keys) to upstream API keys.
	// When a request is authenticated with one of the APIKeys, the corresponding UpstreamAPIKey
	// is used for the upstream Amp request.
	UpstreamAPIKeys []AmpUpstreamAPIKeyEntry `toml:"upstream-api-keys,omitempty" json:"upstream-api-keys,omitempty"`

	// RestrictManagementToLocalhost restricts Amp management routes (/api/user, /api/threads, etc.)
	// to only accept connections from localhost (127.0.0.1, ::1). When true, prevents drive-by
	// browser attacks and remote access to management endpoints. Default: false (API key auth is sufficient).
	RestrictManagementToLocalhost bool `toml:"restrict-management-to-localhost" json:"restrict-management-to-localhost"`

	// ModelMappings defines model name mappings for Amp CLI requests.
	// When Amp requests a model that isn't available locally, these mappings
	// allow routing to an alternative model that IS available.
	ModelMappings []AmpModelMapping `toml:"model-mappings" json:"model-mappings"`

	// ForceModelMappings when true, model mappings take precedence over local API keys.
	// When false (default), local API keys are used first if available.
	ForceModelMappings bool `toml:"force-model-mappings" json:"force-model-mappings"`
}

// AmpUpstreamAPIKeyEntry maps a set of client API keys to a specific upstream API key.
// When a request is authenticated with one of the APIKeys, the corresponding UpstreamAPIKey
// is used for the upstream Amp request.
type AmpUpstreamAPIKeyEntry struct {
	// UpstreamAPIKey is the API key to use when proxying to the Amp upstream.
	UpstreamAPIKey string `toml:"upstream-api-key" json:"upstream-api-key"`

	// APIKeys are the client API keys (from top-level api-keys) that map to this upstream key.
	APIKeys []string `toml:"api-keys" json:"api-keys"`
}

// PayloadConfig defines default and override parameter rules applied to provider payloads.
type PayloadConfig struct {
	// Default defines rules that only set parameters when they are missing in the payload.
	Default []PayloadRule `toml:"default" json:"default"`
	// DefaultRaw defines rules that set raw JSON values only when they are missing.
	DefaultRaw []PayloadRule `toml:"default-raw" json:"default-raw"`
	// Override defines rules that always set parameters, overwriting any existing values.
	Override []PayloadRule `toml:"override" json:"override"`
	// OverrideRaw defines rules that always set raw JSON values, overwriting any existing values.
	OverrideRaw []PayloadRule `toml:"override-raw" json:"override-raw"`
	// Filter defines rules that remove parameters from the payload by JSON path.
	Filter []PayloadFilterRule `toml:"filter" json:"filter"`
}

// PayloadFilterRule describes a rule to remove specific JSON paths from matching model payloads.
type PayloadFilterRule struct {
	// Models lists model entries with name pattern and protocol constraint.
	Models []PayloadModelRule `toml:"models" json:"models"`
	// Params lists JSON paths (gjson/sjson syntax) to remove from the payload.
	Params []string `toml:"params" json:"params"`
}

// PayloadRule describes a single rule targeting a list of models with parameter updates.
type PayloadRule struct {
	// Models lists model entries with name pattern and protocol constraint.
	Models []PayloadModelRule `toml:"models" json:"models"`
	// Params maps JSON paths (gjson/sjson syntax) to values written into the payload.
	// For *-raw rules, values are treated as raw JSON fragments (strings are used as-is).
	Params map[string]any `toml:"params" json:"params"`
}

// PayloadModelRule ties a model name pattern to a specific translator protocol.
type PayloadModelRule struct {
	// Name is the model name or wildcard pattern (e.g., "gpt-*", "*-5", "gemini-*-pro").
	Name string `toml:"name" json:"name"`
	// Protocol restricts the rule to a specific translator format (e.g., "gemini", "responses").
	Protocol string `toml:"protocol" json:"protocol"`
}

// CloakConfig configures request cloaking for non-Claude-Code clients.
// Cloaking disguises API requests to appear as originating from the official Claude Code CLI.
type CloakConfig struct {
	// Mode controls cloaking behavior: "auto" (default), "always", or "never".
	// - "auto": cloak only when client is not Claude Code (based on User-Agent)
	// - "always": always apply cloaking regardless of client
	// - "never": never apply cloaking
	Mode string `toml:"mode,omitempty" json:"mode,omitempty"`

	// StrictMode controls how system prompts are handled when cloaking.
	// - false (default): prepend Claude Code prompt to user system messages
	// - true: strip all user system messages, keep only Claude Code prompt
	StrictMode bool `toml:"strict-mode,omitempty" json:"strict-mode,omitempty"`

	// SensitiveWords is a list of words to obfuscate with zero-width characters.
	// This can help bypass certain content filters.
	SensitiveWords []string `toml:"sensitive-words,omitempty" json:"sensitive-words,omitempty"`

	// CacheUserID controls whether Claude user_id values are cached per API key.
	// When false, a fresh random user_id is generated for every request.
	CacheUserID *bool `toml:"cache-user-id,omitempty" json:"cache-user-id,omitempty"`
}

// ClaudeKey represents the configuration for a Claude API key,
// including the API key itself and an optional base URL for the API endpoint.
type ClaudeKey struct {
	// APIKey is the authentication key for accessing Claude API services.
	APIKey string `toml:"api-key" json:"api-key"`

	// Priority controls selection preference when multiple credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `toml:"priority,omitempty" json:"priority,omitempty"`

	// Prefix optionally namespaces models for this credential (e.g., "teamA/claude-sonnet-4").
	Prefix string `toml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL is the base URL for the Claude API endpoint.
	// If empty, the default Claude API URL will be used.
	BaseURL string `toml:"base-url" json:"base-url"`

	// ProxyURL overrides the global proxy setting for this API key if provided.
	ProxyURL string `toml:"proxy-url" json:"proxy-url"`

	// Models defines upstream model names and aliases for request routing.
	Models []ClaudeModel `toml:"models" json:"models"`

	// Headers optionally adds extra HTTP headers for requests sent with this key.
	Headers map[string]string `toml:"headers,omitempty" json:"headers,omitempty"`

	// ExcludedModels lists model IDs that should be excluded for this provider.
	ExcludedModels []string `toml:"excluded-models,omitempty" json:"excluded-models,omitempty"`

	// Cloak configures request cloaking for non-Claude-Code clients.
	Cloak *CloakConfig `toml:"cloak,omitempty" json:"cloak,omitempty"`

	// ExperimentalCCHSigning enables opt-in final-body cch signing for cloaked
	// Claude /v1/messages requests. It is disabled by default so upstream seed
	// changes do not alter the proxy's legacy behavior.
	ExperimentalCCHSigning bool `toml:"experimental-cch-signing,omitempty" json:"experimental-cch-signing,omitempty"`
}

func (k ClaudeKey) GetAPIKey() string  { return k.APIKey }
func (k ClaudeKey) GetBaseURL() string { return k.BaseURL }

// ClaudeModel describes a mapping between an alias and the actual upstream model name.
type ClaudeModel struct {
	// Name is the upstream model identifier used when issuing requests.
	Name string `toml:"name" json:"name"`

	// Alias is the client-facing model name that maps to Name.
	Alias string `toml:"alias" json:"alias"`
}

func (m ClaudeModel) GetName() string  { return m.Name }
func (m ClaudeModel) GetAlias() string { return m.Alias }

// CodexKey represents the configuration for a Codex API key,
// including the API key itself and an optional base URL for the API endpoint.
type CodexKey struct {
	// APIKey is the authentication key for accessing Codex API services.
	APIKey string `toml:"api-key" json:"api-key"`

	// Priority controls selection preference when multiple credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `toml:"priority,omitempty" json:"priority,omitempty"`

	// Prefix optionally namespaces models for this credential (e.g., "teamA/gpt-5-codex").
	Prefix string `toml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL is the base URL for the Codex API endpoint.
	// If empty, the default Codex API URL will be used.
	BaseURL string `toml:"base-url" json:"base-url"`

	// Websockets enables the Responses API websocket transport for this credential.
	Websockets bool `toml:"websockets,omitempty" json:"websockets,omitempty"`

	// ProxyURL overrides the global proxy setting for this API key if provided.
	ProxyURL string `toml:"proxy-url" json:"proxy-url"`

	// Models defines upstream model names and aliases for request routing.
	Models []CodexModel `toml:"models" json:"models"`

	// Headers optionally adds extra HTTP headers for requests sent with this key.
	Headers map[string]string `toml:"headers,omitempty" json:"headers,omitempty"`

	// ExcludedModels lists model IDs that should be excluded for this provider.
	ExcludedModels []string `toml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
}

func (k CodexKey) GetAPIKey() string  { return k.APIKey }
func (k CodexKey) GetBaseURL() string { return k.BaseURL }

// CodexModel describes a mapping between an alias and the actual upstream model name.
type CodexModel struct {
	// Name is the upstream model identifier used when issuing requests.
	Name string `toml:"name" json:"name"`

	// Alias is the client-facing model name that maps to Name.
	Alias string `toml:"alias" json:"alias"`
}

func (m CodexModel) GetName() string  { return m.Name }
func (m CodexModel) GetAlias() string { return m.Alias }

// GeminiKey represents the configuration for a Gemini API key,
// including optional overrides for upstream base URL, proxy routing, and headers.
type GeminiKey struct {
	// APIKey is the authentication key for accessing Gemini API services.
	APIKey string `toml:"api-key" json:"api-key"`

	// Priority controls selection preference when multiple credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `toml:"priority,omitempty" json:"priority,omitempty"`

	// Prefix optionally namespaces models for this credential (e.g., "teamA/gemini-3-pro-preview").
	Prefix string `toml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL optionally overrides the Gemini API endpoint.
	BaseURL string `toml:"base-url,omitempty" json:"base-url,omitempty"`

	// ProxyURL optionally overrides the global proxy for this API key.
	ProxyURL string `toml:"proxy-url,omitempty" json:"proxy-url,omitempty"`

	// Models defines upstream model names and aliases for request routing.
	Models []GeminiModel `toml:"models,omitempty" json:"models,omitempty"`

	// Headers optionally adds extra HTTP headers for requests sent with this key.
	Headers map[string]string `toml:"headers,omitempty" json:"headers,omitempty"`

	// ExcludedModels lists model IDs that should be excluded for this provider.
	ExcludedModels []string `toml:"excluded-models,omitempty" json:"excluded-models,omitempty"`
}

func (k GeminiKey) GetAPIKey() string  { return k.APIKey }
func (k GeminiKey) GetBaseURL() string { return k.BaseURL }

// GeminiModel describes a mapping between an alias and the actual upstream model name.
type GeminiModel struct {
	// Name is the upstream model identifier used when issuing requests.
	Name string `toml:"name" json:"name"`

	// Alias is the client-facing model name that maps to Name.
	Alias string `toml:"alias" json:"alias"`
}

func (m GeminiModel) GetName() string  { return m.Name }
func (m GeminiModel) GetAlias() string { return m.Alias }

// OpenAICompatibility represents the configuration for OpenAI API compatibility
// with external providers, allowing model aliases to be routed through OpenAI API format.
type OpenAICompatibility struct {
	// Name is the identifier for this OpenAI compatibility configuration.
	Name string `toml:"name" json:"name"`

	// Priority controls selection preference when multiple providers or credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `toml:"priority,omitempty" json:"priority,omitempty"`

	// Prefix optionally namespaces model aliases for this provider (e.g., "teamA/kimi-k2").
	Prefix string `toml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL is the base URL for the external OpenAI-compatible API endpoint.
	BaseURL string `toml:"base-url" json:"base-url"`

	// APIKeyEntries defines API keys with optional per-key proxy configuration.
	APIKeyEntries []OpenAICompatibilityAPIKey `toml:"api-key-entries,omitempty" json:"api-key-entries,omitempty"`

	// Models defines the model configurations including aliases for routing.
	Models []OpenAICompatibilityModel `toml:"models" json:"models"`

	// Headers optionally adds extra HTTP headers for requests sent to this provider.
	Headers map[string]string `toml:"headers,omitempty" json:"headers,omitempty"`
}

// OpenAICompatibilityAPIKey represents an API key configuration with optional proxy setting.
type OpenAICompatibilityAPIKey struct {
	// APIKey is the authentication key for accessing the external API services.
	APIKey string `toml:"api-key" json:"api-key"`

	// ProxyURL overrides the global proxy setting for this API key if provided.
	ProxyURL string `toml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}

// OpenAICompatibilityModel represents a model configuration for OpenAI compatibility,
// including the actual model name and its alias for API routing.
type OpenAICompatibilityModel struct {
	// Name is the actual model name used by the external provider.
	Name string `toml:"name" json:"name"`

	// Alias is the model name alias that clients will use to reference this model.
	Alias string `toml:"alias" json:"alias"`

	// Thinking configures the thinking/reasoning capability for this model.
	// If nil, the model defaults to level-based reasoning with levels ["low", "medium", "high"].
	Thinking *registry.ThinkingSupport `toml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m OpenAICompatibilityModel) GetName() string  { return m.Name }
func (m OpenAICompatibilityModel) GetAlias() string { return m.Alias }

// LoadConfig reads a YAML configuration file from the given path,
// unmarshals it into a Config struct, applies environment variable overrides,
// and returns it.
//
// Parameters:
//   - configFile: The path to the YAML configuration file
//
// Returns:
//   - *Config: The loaded configuration
//   - error: An error if the configuration could not be loaded
func LoadConfig(configFile string) (*Config, error) {
	return LoadConfigOptional(configFile, false)
}

// LoadConfigOptional reads YAML from configFile.
// If optional is true and the file is missing, it returns an empty Config.
// If optional is true and the file is empty or invalid, it returns an empty Config.
func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	// Read the entire configuration file into memory.
	data, err := os.ReadFile(configFile)
	if err != nil {
		if optional {
			if os.IsNotExist(err) || errors.Is(err, syscall.EISDIR) {
				// Missing and optional: return empty config (cloud deploy standby).
				return &Config{}, nil
			}
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// In cloud deploy mode (optional=true), if file is empty or contains only whitespace, return empty config.
	if optional && len(data) == 0 {
		return &Config{}, nil
	}

	// Unmarshal the YAML data into the Config struct.
	var cfg Config
	// Set defaults before unmarshal so that absent keys keep defaults.
	cfg.Host = "" // Default empty: binds to all interfaces (IPv4 + IPv6)
	cfg.LoggingToFile = false
	cfg.LogsMaxTotalSizeMB = 0
	cfg.ErrorLogsMaxFiles = 10
	cfg.UsageStatisticsEnabled = false
	cfg.DisableCooling = false
	cfg.Pprof.Enable = false
	cfg.Pprof.Addr = DefaultPprofAddr
	cfg.AmpCode.RestrictManagementToLocalhost = false // Default to false: API key auth is sufficient
	cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	if err = toml.Unmarshal(data, &cfg); err != nil {
		if optional {
			// In cloud deploy mode, if TOML parsing fails, return empty config instead of error.
			return &Config{}, nil
		}
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Hash remote management key if plaintext is detected (nested)
	// We consider a value to be already hashed if it looks like a bcrypt hash ($2a$, $2b$, or $2y$ prefix).
	if cfg.RemoteManagement.SecretKey != "" && !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) {
		hashed, errHash := hashSecret(cfg.RemoteManagement.SecretKey)
		if errHash != nil {
			return nil, fmt.Errorf("failed to hash remote management key: %w", errHash)
		}
		cfg.RemoteManagement.SecretKey = hashed

		// Persist the hashed value back to config.toml via a targeted line edit so
		// comments and field ordering are preserved.
		_ = updateTOMLScalarInPlace(configFile, "secret-key", hashed)
	}

	cfg.RemoteManagement.PanelGitHubRepository = strings.TrimSpace(cfg.RemoteManagement.PanelGitHubRepository)
	if cfg.RemoteManagement.PanelGitHubRepository == "" {
		cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	}

	cfg.Pprof.Addr = strings.TrimSpace(cfg.Pprof.Addr)
	if cfg.Pprof.Addr == "" {
		cfg.Pprof.Addr = DefaultPprofAddr
	}

	if cfg.LogsMaxTotalSizeMB < 0 {
		cfg.LogsMaxTotalSizeMB = 0
	}

	if cfg.ErrorLogsMaxFiles < 0 {
		cfg.ErrorLogsMaxFiles = 10
	}

	if cfg.MaxRetryCredentials < 0 {
		cfg.MaxRetryCredentials = 0
	}

	// Sanitize Gemini API key configuration and migrate legacy entries.
	cfg.SanitizeGeminiKeys()

	// Sanitize Vertex-compatible API keys.
	cfg.SanitizeVertexCompatKeys()

	// Sanitize Codex keys: drop entries without base-url
	cfg.SanitizeCodexKeys()

	// Sanitize Codex header defaults.
	cfg.SanitizeCodexHeaderDefaults()

	// Sanitize Claude header defaults.
	cfg.SanitizeClaudeHeaderDefaults()

	// Sanitize Claude key headers
	cfg.SanitizeClaudeKeys()

	// Sanitize OpenAI compatibility providers: drop entries without base-url
	cfg.SanitizeOpenAICompatibility()

	// Normalize OAuth provider model exclusion map.
	cfg.OAuthExcludedModels = NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)

	// Normalize global OAuth model name aliases.
	cfg.SanitizeOAuthModelAlias()

	// Validate raw payload rules and drop invalid entries.
	cfg.SanitizePayloadRules()

	// NOTE: Legacy migration persistence is intentionally disabled together with
	// startup legacy migration to keep startup read-only for config.toml.
	// Re-enable the block below if automatic startup migration is needed again.
	// if cfg.legacyMigrationPending {
	// 	fmt.Println("Detected legacy configuration keys, attempting to persist the normalized config...")
	// 	if !optional && configFile != "" {
	// 		if err := SaveConfigPreserveComments(configFile, &cfg); err != nil {
	// 			return nil, fmt.Errorf("failed to persist migrated legacy config: %w", err)
	// 		}
	// 		fmt.Println("Legacy configuration normalized and persisted.")
	// 	} else {
	// 		fmt.Println("Legacy configuration normalized in memory; persistence skipped.")
	// 	}
	// }

	// Return the populated configuration struct.
	return &cfg, nil
}

// SanitizePayloadRules validates raw JSON payload rule params and drops invalid rules.
func (cfg *Config) SanitizePayloadRules() {
	if cfg == nil {
		return
	}
	cfg.Payload.DefaultRaw = sanitizePayloadRawRules(cfg.Payload.DefaultRaw, "default-raw")
	cfg.Payload.OverrideRaw = sanitizePayloadRawRules(cfg.Payload.OverrideRaw, "override-raw")
}

func sanitizePayloadRawRules(rules []PayloadRule, section string) []PayloadRule {
	if len(rules) == 0 {
		return rules
	}
	out := make([]PayloadRule, 0, len(rules))
	for i := range rules {
		rule := rules[i]
		if len(rule.Params) == 0 {
			continue
		}
		invalid := false
		for path, value := range rule.Params {
			raw, ok := payloadRawString(value)
			if !ok {
				continue
			}
			trimmed := bytes.TrimSpace(raw)
			if len(trimmed) == 0 || !json.Valid(trimmed) {
				log.WithFields(log.Fields{
					"section":    section,
					"rule_index": i + 1,
					"param":      path,
				}).Warn("payload rule dropped: invalid raw JSON")
				invalid = true
				break
			}
		}
		if invalid {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func payloadRawString(value any) ([]byte, bool) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		return nil, false
	}
}

// SanitizeCodexHeaderDefaults trims surrounding whitespace from the
// configured Codex header fallback values.
func (cfg *Config) SanitizeCodexHeaderDefaults() {
	if cfg == nil {
		return
	}
	cfg.CodexHeaderDefaults.UserAgent = strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent)
	cfg.CodexHeaderDefaults.BetaFeatures = strings.TrimSpace(cfg.CodexHeaderDefaults.BetaFeatures)
}

// SanitizeClaudeHeaderDefaults trims surrounding whitespace from the
// configured Claude fingerprint baseline values.
func (cfg *Config) SanitizeClaudeHeaderDefaults() {
	if cfg == nil {
		return
	}
	cfg.ClaudeHeaderDefaults.UserAgent = strings.TrimSpace(cfg.ClaudeHeaderDefaults.UserAgent)
	cfg.ClaudeHeaderDefaults.PackageVersion = strings.TrimSpace(cfg.ClaudeHeaderDefaults.PackageVersion)
	cfg.ClaudeHeaderDefaults.RuntimeVersion = strings.TrimSpace(cfg.ClaudeHeaderDefaults.RuntimeVersion)
	cfg.ClaudeHeaderDefaults.OS = strings.TrimSpace(cfg.ClaudeHeaderDefaults.OS)
	cfg.ClaudeHeaderDefaults.Arch = strings.TrimSpace(cfg.ClaudeHeaderDefaults.Arch)
	cfg.ClaudeHeaderDefaults.Timeout = strings.TrimSpace(cfg.ClaudeHeaderDefaults.Timeout)
}

// SanitizeOAuthModelAlias normalizes and deduplicates global OAuth model name aliases.
// It trims whitespace, normalizes channel keys to lower-case, drops empty entries,
// allows multiple aliases per upstream name, and ensures aliases are unique within each channel.
func (cfg *Config) SanitizeOAuthModelAlias() {
	if cfg == nil || len(cfg.OAuthModelAlias) == 0 {
		return
	}
	out := make(map[string][]OAuthModelAlias, len(cfg.OAuthModelAlias))
	for rawChannel, aliases := range cfg.OAuthModelAlias {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(aliases) == 0 {
			continue
		}
		seenAlias := make(map[string]struct{}, len(aliases))
		clean := make([]OAuthModelAlias, 0, len(aliases))
		for _, entry := range aliases {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			if _, ok := seenAlias[aliasKey]; ok {
				continue
			}
			seenAlias[aliasKey] = struct{}{}
			clean = append(clean, OAuthModelAlias{Name: name, Alias: alias, Fork: entry.Fork})
		}
		if len(clean) > 0 {
			out[channel] = clean
		}
	}
	cfg.OAuthModelAlias = out
}

// SanitizeOpenAICompatibility removes OpenAI-compatibility provider entries that are
// not actionable, specifically those missing a BaseURL. It trims whitespace before
// evaluation and preserves the relative order of remaining entries.
func (cfg *Config) SanitizeOpenAICompatibility() {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return
	}
	out := make([]OpenAICompatibility, 0, len(cfg.OpenAICompatibility))
	for i := range cfg.OpenAICompatibility {
		e := cfg.OpenAICompatibility[i]
		e.Name = strings.TrimSpace(e.Name)
		e.Prefix = normalizeModelPrefix(e.Prefix)
		e.BaseURL = strings.TrimSpace(e.BaseURL)
		e.Headers = NormalizeHeaders(e.Headers)
		if e.BaseURL == "" {
			// Skip providers with no base-url; treated as removed
			continue
		}
		out = append(out, e)
	}
	cfg.OpenAICompatibility = out
}

// SanitizeCodexKeys removes Codex API key entries missing a BaseURL.
// It trims whitespace and preserves order for remaining entries.
func (cfg *Config) SanitizeCodexKeys() {
	if cfg == nil || len(cfg.CodexKey) == 0 {
		return
	}
	out := make([]CodexKey, 0, len(cfg.CodexKey))
	for i := range cfg.CodexKey {
		e := cfg.CodexKey[i]
		e.Prefix = normalizeModelPrefix(e.Prefix)
		e.BaseURL = strings.TrimSpace(e.BaseURL)
		e.Headers = NormalizeHeaders(e.Headers)
		e.ExcludedModels = NormalizeExcludedModels(e.ExcludedModels)
		if e.BaseURL == "" {
			continue
		}
		out = append(out, e)
	}
	cfg.CodexKey = out
}

// SanitizeClaudeKeys normalizes headers for Claude credentials.
func (cfg *Config) SanitizeClaudeKeys() {
	if cfg == nil || len(cfg.ClaudeKey) == 0 {
		return
	}
	for i := range cfg.ClaudeKey {
		entry := &cfg.ClaudeKey[i]
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
	}
}

// SanitizeGeminiKeys deduplicates and normalizes Gemini credentials.
// It uses API key + base URL as the uniqueness key.
func (cfg *Config) SanitizeGeminiKeys() {
	if cfg == nil {
		return
	}

	seen := make(map[string]struct{}, len(cfg.GeminiKey))
	out := cfg.GeminiKey[:0]
	for i := range cfg.GeminiKey {
		entry := cfg.GeminiKey[i]
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		if entry.APIKey == "" {
			continue
		}
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = NormalizeHeaders(entry.Headers)
		entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
		uniqueKey := entry.APIKey + "|" + entry.BaseURL
		if _, exists := seen[uniqueKey]; exists {
			continue
		}
		seen[uniqueKey] = struct{}{}
		out = append(out, entry)
	}
	cfg.GeminiKey = out
}

func normalizeModelPrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "/") {
		return ""
	}
	return trimmed
}

// looksLikeBcrypt returns true if the provided string appears to be a bcrypt hash.
func looksLikeBcrypt(s string) bool {
	return len(s) > 4 && (s[:4] == "$2a$" || s[:4] == "$2b$" || s[:4] == "$2y$")
}

// NormalizeHeaders trims header keys and values and removes empty pairs.
func NormalizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	clean := make(map[string]string, len(headers))
	for k, v := range headers {
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		clean[key] = val
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

// NormalizeExcludedModels trims, lowercases, and deduplicates model exclusion patterns.
// It preserves the order of first occurrences and drops empty entries.
func NormalizeExcludedModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, raw := range models {
		trimmed := strings.ToLower(strings.TrimSpace(raw))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeOAuthExcludedModels cleans provider -> excluded models mappings by normalizing provider keys
// and applying model exclusion normalization to each entry.
func NormalizeOAuthExcludedModels(entries map[string][]string) map[string][]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string][]string, len(entries))
	for provider, models := range entries {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" {
			continue
		}
		normalized := NormalizeExcludedModels(models)
		if len(normalized) == 0 {
			continue
		}
		out[key] = normalized
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// hashSecret hashes the given secret using bcrypt.
func hashSecret(secret string) (string, error) {
	// Use default cost for simplicity.
	hashedBytes, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashedBytes), nil
}


// SaveConfig marshals the in-memory config into TOML and writes it atomically
// to configFile. Programmatic writes lose user comments on the keys that get
// re-emitted by the marshaller; callers that want full comment preservation
// should have the user edit config.toml directly (and rely on fsnotify reload).
func SaveConfig(configFile string, cfg *Config) error {
	if cfg == nil {
		return errors.New("cfg is nil")
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	tmp := configFile + ".tmp"
	if err = os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}
	if err = os.Rename(tmp, configFile); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to swap config into place: %w", err)
	}
	return nil
}

// updateTOMLScalarInPlace does a line-level regex replacement for a single scalar
// key in configFile, preserving every other byte (comments, blank lines, ordering).
// key must be the bare TOML key name (e.g. "secret-key"). The new value is written
// as a double-quoted string regardless of whether the original used single or
// double quotes. If the key is not found the file is left untouched and nil is
// returned — callers treat first-boot hashing as best-effort.
func updateTOMLScalarInPlace(configFile, key, value string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	// Match `  key = "..."` or `  key = '...'` with any indentation; keep leading
	// whitespace and the `key = ` prefix, replace only the quoted value.
	pattern := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(key) + `\s*=\s*)(?:"(?:\\.|[^"\\])*"|'[^']*')`)
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	replacement := []byte("${1}\"" + escaped + "\"")
	updated := pattern.ReplaceAll(data, replacement)
	if len(updated) == len(data) {
		return nil // key not present; leave file alone
	}
	return os.WriteFile(configFile, updated, 0644)
}
