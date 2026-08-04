package imageopt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const DeploymentConfigPath = ".gobeyond/images.json"

// DeploymentConfig is the repo-owned image configuration. The deployment
// pipeline converts it to runtime environment configuration after validation.
type DeploymentConfig struct {
	RemoteDomains []string `json:"remoteDomains"`
}

// LoadDeploymentConfig reads the optional repo-owned image configuration.
// Missing configuration means that the deployment has no remote image
// sources; same-site disk/S3 sources remain available.
func LoadDeploymentConfig(root string) (DeploymentConfig, bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, DeploymentConfigPath))
	if errors.Is(err, os.ErrNotExist) {
		return DeploymentConfig{}, false, nil
	}
	if err != nil {
		return DeploymentConfig{}, false, fmt.Errorf("read %s: %w", DeploymentConfigPath, err)
	}
	var config DeploymentConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return DeploymentConfig{}, false, fmt.Errorf("decode %s: %w", DeploymentConfigPath, err)
	}
	loader, err := NewRemoteLoader(config.RemoteDomains)
	if err != nil {
		return DeploymentConfig{}, false, fmt.Errorf("validate %s: %w", DeploymentConfigPath, err)
	}
	_ = loader
	return config, true, nil
}
