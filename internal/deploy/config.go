package deploy

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultConfigPath is used when CONFIG_FILE is not set.
const DefaultConfigPath = "deploy/relay.env"

// Config represents the relay deploy configuration.
type Config struct {
	AppName         string
	DeployHost      string
	SSHKey          string
	SSHPort         string
	SSHBatchMode    bool
	BuildWorkdir    string
	BuildPackage    string
	BlueAddr        string
	GreenAddr       string
	BlueHealthAddr  string
	GreenHealthAddr string
	PublicAddr      string
	RelayPublicAddr string
	RelayStateDir   string
	LogFormat       string
	LogLevel        string
}

// MustLoad loads CONFIG_FILE or the default path and exits on error.
func MustLoad() *Config {
	cfg, err := Load(PathFromEnv())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

// Load reads relay deploy configuration from a KEY=VALUE file.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath
	}

	values, err := readEnvFile(path)
	if err != nil {
		return nil, err
	}

	value := func(key string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return values[key]
	}

	appName := valueOr(value("APP_NAME"), "yap-relay")
	cfg := &Config{
		AppName:         appName,
		DeployHost:      value("DEPLOY_HOST"),
		SSHKey:          value("SSH_KEY"),
		SSHPort:         valueOr(value("SSH_PORT"), "22"),
		SSHBatchMode:    valueOrBool(value("SSH_BATCH_MODE"), false),
		BuildWorkdir:    valueOr(value("BUILD_WORKDIR"), "."),
		BuildPackage:    valueOr(value("BUILD_PACKAGE"), "./cmd/yap-relay"),
		BlueAddr:        valueOr(value("BLUE_ADDR"), "127.0.0.1:18081"),
		GreenAddr:       valueOr(value("GREEN_ADDR"), "127.0.0.1:18082"),
		BlueHealthAddr:  valueOr(value("BLUE_HEALTH_ADDR"), "127.0.0.1:19081"),
		GreenHealthAddr: valueOr(value("GREEN_HEALTH_ADDR"), "127.0.0.1:19082"),
		PublicAddr:      valueOr(value("PUBLIC_ADDR"), ":4001"),
		RelayPublicAddr: strings.TrimSpace(value("YAP_RELAY_PUBLIC_ADDR")),
		RelayStateDir:   valueOr(value("YAP_RELAY_STATE_DIR"), filepath.ToSlash(filepath.Join("/var/lib", appName, "state"))),
		LogFormat:       valueOr(value("LOG_FORMAT"), "text"),
		LogLevel:        valueOr(value("LOG_LEVEL"), "info"),
	}

	return cfg, nil
}

// PathFromEnv returns CONFIG_FILE or the default relay config path.
func PathFromEnv() string {
	if path := os.Getenv("CONFIG_FILE"); strings.TrimSpace(path) != "" {
		return path
	}
	return DefaultConfigPath
}

// EnsureRequired returns an error when required fields are missing.
func (c *Config) EnsureRequired(fields ...string) error {
	var missing []string
	for _, field := range fields {
		switch field {
		case "APP_NAME":
			if strings.TrimSpace(c.AppName) == "" {
				missing = append(missing, field)
			}
		case "DEPLOY_HOST":
			if strings.TrimSpace(c.DeployHost) == "" {
				missing = append(missing, field)
			}
		case "YAP_RELAY_PUBLIC_ADDR":
			if strings.TrimSpace(c.RelayPublicAddr) == "" {
				missing = append(missing, field)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid line in %s: %q", path, line)
		}

		key := strings.TrimSpace(parts[0])
		values[key] = parseValue(parts[1])
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return values, nil
}

func valueOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func valueOrBool(v string, def bool) bool {
	if strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func parseValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
