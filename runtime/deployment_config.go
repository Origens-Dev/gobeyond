package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const DeploymentRevisionHeader = "X-GoBeyond-Deployment"

// deploymentConfig contains only explicitly public values. The platform passes
// this JSON independently of encrypted server/worker variables.
func loadDeploymentConfig(config *Config) error {
	if config.DeploymentRevision == "" {
		config.DeploymentRevision = strings.TrimSpace(os.Getenv("GOBEYOND_DEPLOYMENT_REVISION"))
	}
	if config.PublicConfig == nil {
		if raw := os.Getenv("GOBEYOND_PUBLIC_CONFIG"); raw != "" {
			if len(raw) > 64<<10 {
				return fmt.Errorf("public runtime configuration exceeds 64 KiB")
			}
			if err := json.Unmarshal([]byte(raw), &config.PublicConfig); err != nil {
				return fmt.Errorf("invalid public runtime configuration: %w", err)
			}
		}
	}
	if len(config.PublicConfig) > 0 && config.DeploymentRevision == "" {
		return fmt.Errorf("public runtime configuration requires deployment revision")
	}
	// Own the snapshot; callers cannot mutate live configuration after New.
	snapshot := make(map[string]string, len(config.PublicConfig))
	for k, v := range config.PublicConfig {
		snapshot[k] = v
	}
	config.PublicConfig = snapshot
	return nil
}
