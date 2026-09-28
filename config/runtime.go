package config

import "context"

// Runtime holds a validated local configuration for one process. Restart the
// process after changing the file; no external configuration service is used.
type Runtime struct{ Config *Config }

func (r *Runtime) Close() {}
func Open(_ context.Context, path string) (*Runtime, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Runtime{Config: cfg}, nil
}
func LoadStartup(path string) (*Config, error) { return Load(path) }
