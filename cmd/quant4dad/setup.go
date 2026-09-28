package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/quant4dad/config"
	"gopkg.in/yaml.v3"
)

// Setup is an explicit offline operation, run inside the API image by install.sh.
// Environment parameters apply only to setup; running services use their YAML.
func runSetup(directory string) int {
	if err := setupStandalone(directory); err != nil {
		os.Stderr.WriteString("standalone_setup_failed: existing files must be private and valid; check configuration and directory ownership\n")
		return 1
	}
	os.Stdout.WriteString("standalone_configuration_ready\n")
	return 0
}

func setupStandalone(directory string) error {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if err = privateDirectory(directory); err != nil {
		return err
	}
	configDir := filepath.Join(directory, "config")
	if err = privateDirectory(configDir); err != nil {
		return err
	}
	if err = privateDirectory(filepath.Join(directory, "data")); err != nil {
		return err
	}
	apiPath := filepath.Join(configDir, "api.yaml")
	cfg, err := config.Parse([]byte("server:\n  addr: ':8080'\nweb:\n  api_base_url: http://api:8080\nstorage:\n  backend: sqlite\n  sqlite:\n    path: /app/data/quant4dad.db\nauto_sync:\n  enabled: false\nnews:\n  enabled: false\narchive:\n  enabled: false\n"))
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(apiPath); statErr == nil {
		body, readErr := readPrivate(apiPath)
		if readErr != nil {
			return readErr
		}
		cfg, err = config.Parse(body)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	// Preserve the shared artifact location when adding a second extension.
	resultDirectory := cfg.MCP.ToolResultDirectory()
	if cfg.Agent.Gateway.Enabled {
		resultDirectory = cfg.Agent.Gateway.ResultDirectory
	}
	login, err := setupSecret(filepath.Join(configDir, "login-token"), cfg.Security.Token, false)
	if err != nil {
		return err
	}
	cfg.Security.Token = login
	if source := os.Getenv("Q4D_SETUP_MYSQL_DSN_FILE"); source != "" {
		body, readErr := os.ReadFile(source)
		if readErr != nil || len(body) > 16*1024 {
			return errors.New("invalid mysql input")
		}
		dsn := strings.TrimSpace(string(body))
		if dsn == "" {
			return errors.New("empty mysql input")
		}
		cfg.Storage.Backend, cfg.Storage.MySQL.DSN = config.StorageBackendMySQL, dsn
	}
	if os.Getenv("Q4D_SETUP_MCP") == "1" {
		cfg.MCP.AgentToolsEnabled = true
	}
	if cfg.MCP.AgentToolsEnabled {
		cfg.MCP.ExternalToken, err = setupSecret(filepath.Join(configDir, "mcp-token"), cfg.MCP.ExternalToken, false)
		if err != nil {
			return err
		}
		cfg.MCP.SigningPrivateKey, err = setupSecret(filepath.Join(configDir, "mcp-signing-key"), cfg.MCP.SigningPrivateKey, true)
		if err != nil {
			return err
		}
		cfg.MCP.APIBaseURL, cfg.MCP.ResultDirectory = "http://api:8080", resultDirectory
	}
	if os.Getenv("Q4D_SETUP_AGENT") == "1" {
		body, readErr := readPrivate(filepath.Join(directory, "agent", "config", "agent-fragment.json"))
		if readErr != nil {
			return readErr
		}
		var fragment struct {
			Agent config.AgentConfig `yaml:"agent"`
		}
		if err = yaml.Unmarshal(body, &fragment); err != nil {
			return err
		}
		if !fragment.Agent.Enabled {
			return errors.New("missing agent configuration")
		}
		cfg.Agent = fragment.Agent
		cfg.Agent.Gateway.ResultDirectory = resultDirectory
	}
	// Validate the complete API configuration before replacing any config file.
	apiBody, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if _, err = config.Parse(apiBody); err != nil {
		return err
	}
	webPath := filepath.Join(configDir, "web.yaml")
	webConfig := config.WebConfig{Addr: ":8080", APIBaseURL: "http://api:8080"}
	if _, statErr := os.Lstat(webPath); statErr == nil {
		body, readErr := readPrivate(webPath)
		if readErr != nil {
			return readErr
		}
		previous, parseErr := config.Parse(body)
		if parseErr != nil {
			return parseErr
		}
		webConfig = previous.Web
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	// The Web process only receives its routing configuration, even when an
	// operator previously used a complete API configuration for this file.
	webBody, err := yaml.Marshal(struct {
		Web config.WebConfig `yaml:"web"`
	}{webConfig})
	if err != nil {
		return err
	}
	if _, err = config.Parse(webBody); err != nil {
		return err
	}
	var mcpBody []byte
	if cfg.MCP.AgentToolsEnabled {
		proxy := cfg.MCP
		proxy.SigningPrivateKey, proxy.ResultDirectory, proxy.MySQLDSN = "", "", ""
		mcpBody, err = yaml.Marshal(struct {
			MCP config.MCPConfig `yaml:"mcp"`
		}{proxy})
		if err != nil {
			return err
		}
		if _, err = config.Parse(mcpBody); err != nil {
			return err
		}
	}
	if err = replacePrivate(apiPath, apiBody); err != nil {
		return err
	}
	if err = replacePrivate(filepath.Join(configDir, "web.yaml"), webBody); err != nil {
		return err
	}
	if cfg.MCP.AgentToolsEnabled {
		if err = replacePrivate(filepath.Join(configDir, "mcp.yaml"), mcpBody); err != nil {
			return err
		}
	}
	profiles := ""
	if cfg.MCP.AgentToolsEnabled {
		profiles += "mcp\n"
	}
	if cfg.Agent.Enabled {
		profiles += "agent\n"
	}
	return replacePrivate(filepath.Join(directory, "profiles"), []byte(profiles))
}

func privateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("directory must have mode 0700")
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil || actual != path {
		return errors.New("directory must not contain symlinks")
	}
	return nil
}
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1024*1024 {
		return nil, errors.New("file must be private and regular")
	}
	return os.ReadFile(path)
}
func setupSecret(path, current string, signing bool) (string, error) {
	if body, err := readPrivate(path); err == nil {
		value := strings.TrimSpace(string(body))
		if len(value) < 32 || strings.ContainsAny(value, "\r\n\t ") || current != "" && value != current {
			return "", errors.New("invalid existing secret")
		}
		if signing {
			key, err := base64.RawURLEncoding.Strict().DecodeString(value)
			if err != nil || len(key) != ed25519.PrivateKeySize {
				return "", errors.New("invalid signing key")
			}
		}
		return value, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	value := current
	if value == "" {
		if signing {
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return "", err
			}
			value = base64.RawURLEncoding.EncodeToString(key)
		} else {
			random := make([]byte, 48)
			if _, err := rand.Read(random); err != nil {
				return "", err
			}
			value = base64.RawURLEncoding.EncodeToString(random)
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, err = file.WriteString(value + "\n")
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	return value, closeErr
}
func replacePrivate(path string, body []byte) error {
	if _, err := os.Lstat(path); err == nil {
		if _, err = readPrivate(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".setup-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
