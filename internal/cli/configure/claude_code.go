package configure

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	cliruntime "github.com/agentregistry-dev/agentregistry/pkg/cli/runtime"
)

const (
	claudeSettingsLocalPath      = ".claude/settings.local.json"
	agentRegistryMarketplaceName = "agentregistry"
	marketplaceHeadersHelper     = "arctl configure claude-code marketplace-headers"
)

// ClaudeCodeConfigurer handles Claude Code MCP configuration
type ClaudeCodeConfigurer struct{}

// claudeServerConfig is the arctl server entry written into .mcp.json. Other
// servers' entries are preserved verbatim by mergeServerEntry to avoid breaking
// a user's file.
type claudeServerConfig struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type claudeMarketplaceConfig struct {
	Source claudeMarketplaceSource `json:"source"`
}

type claudeMarketplaceSource struct {
	Source        string `json:"source"`
	URL           string `json:"url"`
	HeadersHelper string `json:"headersHelper"`
}

func (c *ClaudeCodeConfigurer) GetConfigPath() (string, error) {
	return ".mcp.json", nil
}

func (c *ClaudeCodeConfigurer) CreateConfig(opts CreateOptions, configPath string) (any, error) {
	entry := claudeServerConfig{
		Type: "http",
		URL:  opts.URL,
	}
	if opts.TokenEnv != "" {
		// Claude Code expands ${VAR} in header values at connect time, keeping the token out of the checked-in file.
		entry.Headers = map[string]string{
			"Authorization": fmt.Sprintf("Bearer ${%s}", opts.TokenEnv),
		}
	}
	return mergeServerEntry(configPath, "mcpServers", entry)
}

func (c *ClaudeCodeConfigurer) GetClientName() string {
	return "Claude Code"
}

func configureClaudeCodeMarketplace(cmd *cobra.Command, rt cliruntime.Runtime) {
	var enabled bool
	var marketplaceURL string
	configureClient := cmd.RunE

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !enabled && marketplaceURL == "" {
			return configureClient(cmd, args)
		}
		if rt == nil {
			return fmt.Errorf("registry runtime is not configured")
		}

		target, err := rt.ResolveRegistryTarget(cmd.Context())
		if err != nil {
			return fmt.Errorf("resolving plugin marketplace URL: %w", err)
		}
		if marketplaceURL == "" {
			marketplaceURL = defaultPluginMarketplaceURL(target.BaseURL)
		}
		marketplaceConfig, err := createClaudeMarketplaceConfig(marketplaceURL, claudeSettingsLocalPath)
		if err != nil {
			return fmt.Errorf("creating Claude Code plugin marketplace config: %w", err)
		}

		if err := configureClient(cmd, args); err != nil {
			return err
		}
		if err := writeConfigFile(claudeSettingsLocalPath, marketplaceConfig); err != nil {
			return fmt.Errorf("writing plugin marketplace config: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Configured AgentRegistry plugin marketplace")
		return nil
	}

	cmd.Flags().BoolVar(&enabled, "plugin-marketplace", false, "Add the AgentRegistry plugin marketplace using --registry-url")
	cmd.Flags().StringVar(&marketplaceURL, "plugin-marketplace-url", "", "Add the AgentRegistry plugin marketplace using a custom URL")
	cmd.AddCommand(newMarketplaceHeadersCommand(rt))
}

func newMarketplaceHeadersCommand(rt cliruntime.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:    "marketplace-headers",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rt == nil {
				return fmt.Errorf("registry runtime is not configured")
			}
			target, err := rt.ResolveRegistryTarget(cmd.Context())
			if err != nil {
				return fmt.Errorf("resolving registry credentials: %w", err)
			}
			headers := map[string]string{}
			if target.Token != "" {
				headers["Authorization"] = "Bearer " + target.Token
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(headers); err != nil {
				return fmt.Errorf("writing marketplace headers: %w", err)
			}
			return nil
		},
	}
}

func defaultPluginMarketplaceURL(registryURL string) string {
	return strings.TrimRight(registryURL, "/") + "/plugin-marketplace/marketplace.json"
}

func createClaudeMarketplaceConfig(marketplaceURL, configPath string) (any, error) {
	entry := claudeMarketplaceConfig{
		Source: claudeMarketplaceSource{
			Source:        "url",
			URL:           marketplaceURL,
			HeadersHelper: marketplaceHeadersHelper,
		},
	}
	return mergeNamedEntry(configPath, "extraKnownMarketplaces", agentRegistryMarketplaceName, entry)
}
