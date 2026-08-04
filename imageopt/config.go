package imageopt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const DeploymentConfigPath = ".gobeyond/images.json"

const (
	DefaultCacheSeconds  = 3600
	MinCacheSeconds      = 60
	MaxCacheSeconds      = 31536000
	ImageCacheSecondsEnv = "GOBEYOND_IMAGE_CACHE_SECONDS"
)

// DeploymentConfig is the repo-owned image configuration. The deployment
// pipeline converts it to runtime environment configuration after validation.
type DeploymentConfig struct {
	RemoteDomains []string `json:"remoteDomains"`
	CacheSeconds  int      `json:"cacheSeconds,omitempty"`
}

func NormalizeCacheSeconds(value int) (int, error) {
	if value == 0 {
		return DefaultCacheSeconds, nil
	}
	if value < MinCacheSeconds || value > MaxCacheSeconds {
		return 0, fmt.Errorf("cacheSeconds must be between %d and %d seconds", MinCacheSeconds, MaxCacheSeconds)
	}
	return value, nil
}

func CacheSecondsFromEnvironment() int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(ImageCacheSecondsEnv)))
	if err != nil || value < MinCacheSeconds || value > MaxCacheSeconds {
		return DefaultCacheSeconds
	}
	return value
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
	cacheSeconds, err := NormalizeCacheSeconds(config.CacheSeconds)
	if err != nil {
		return DeploymentConfig{}, false, fmt.Errorf("validate %s: %w", DeploymentConfigPath, err)
	}
	config.CacheSeconds = cacheSeconds
	loader, err := NewRemoteLoader(config.RemoteDomains)
	if err != nil {
		return DeploymentConfig{}, false, fmt.Errorf("validate %s: %w", DeploymentConfigPath, err)
	}
	_ = loader
	return config, true, nil
}
