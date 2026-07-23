package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ModeDefaults struct {
	ModeDefaults map[string]string `yaml:"mode_defaults"`
}

type ModeModelResolver struct {
	SessionModel string
	ProjectPath  string
	GlobalPath   string
}

func NewModeModelResolver(projectRoot string) *ModeModelResolver {
	home, _ := os.UserHomeDir()
	return &ModeModelResolver{
		ProjectPath: filepath.Join(projectRoot, ".izen", "config.yaml"),
		GlobalPath:  filepath.Join(home, ".izen", "config.yml"),
	}
}

func (r *ModeModelResolver) SetSessionModel(model string) {
	r.SessionModel = model
}

func (r *ModeModelResolver) ClearSessionModel() {
	r.SessionModel = ""
}

func (r *ModeModelResolver) ResolveModelForMode(mode string) string {
	if r.SessionModel != "" {
		return r.SessionModel
	}

	if model := r.readProjectModeDefault(mode); model != "" {
		return model
	}

	if model := r.readGlobalModeDefault(mode); model != "" {
		return model
	}

	return ""
}

func (r *ModeModelResolver) readProjectModeDefault(mode string) string {
	data, err := os.ReadFile(r.ProjectPath)
	if err != nil {
		return ""
	}

	var cfg struct {
		Models struct {
			ModeDefaults map[string]string `yaml:"mode_defaults"`
		} `yaml:"models"`
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}

	if cfg.Models.ModeDefaults != nil {
		return cfg.Models.ModeDefaults[mode]
	}

	return ""
}

func (r *ModeModelResolver) readGlobalModeDefault(mode string) string {
	data, err := os.ReadFile(r.GlobalPath)
	if err != nil {
		return ""
	}

	var cfg struct {
		Models struct {
			ModeDefaults map[string]string `yaml:"mode_defaults"`
		} `yaml:"models"`
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}

	if cfg.Models.ModeDefaults != nil {
		return cfg.Models.ModeDefaults[mode]
	}

	return ""
}
