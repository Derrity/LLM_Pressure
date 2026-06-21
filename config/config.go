package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Profile 是一组 base url + api key 的命名配置
type Profile struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	LastModel string `json:"last_model,omitempty"`
}

// File 是 config.json 的根结构
type File struct {
	Profiles []Profile `json:"profiles"`
	Default  string    `json:"default,omitempty"`
}

// Path 返回当前目录下的 config.json 路径
func Path() string {
	p, err := os.Getwd()
	if err != nil {
		p = "."
	}
	return filepath.Join(p, "config.json")
}

// Load 读取 config.json；文件不存在时返回空配置（不视为错误）
func Load() (*File, error) {
	f := &File{}
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return nil, fmt.Errorf("读取 config.json 失败: %w", err)
	}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("解析 config.json 失败: %w", err)
	}
	return f, nil
}

// Save 将配置写回 config.json（权限 0600，避免 key 被 group/other 读取）
func (f *File) Save() error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(Path(), data, 0o600); err != nil {
		return fmt.Errorf("写入 config.json 失败: %w", err)
	}
	return nil
}

// AddProfile 追加一个 profile，并设为默认（同名覆盖旧值）
func (f *File) AddProfile(p Profile) {
	replaced := false
	for i, existing := range f.Profiles {
		if existing.Name == p.Name {
			f.Profiles[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		f.Profiles = append(f.Profiles, p)
	}
	f.Default = p.Name
}

// DefaultProfile 返回当前默认 profile；若 default 字段为空或失效，则返回第一个 profile。
func (f *File) DefaultProfile() (Profile, bool) {
	if f.Default != "" {
		if p, ok := f.Find(f.Default); ok {
			return p, true
		}
	}
	if len(f.Profiles) > 0 {
		return f.Profiles[0], true
	}
	return Profile{}, false
}

// SetDefault 将已存在的 profile 设为默认。
func (f *File) SetDefault(name string) bool {
	for _, p := range f.Profiles {
		if p.Name == name {
			f.Default = name
			return true
		}
	}
	return false
}

// SetLastModel 记录某个 profile 最近使用过的模型，便于参数模式直接运行。
func (f *File) SetLastModel(profileName, model string) bool {
	for i, p := range f.Profiles {
		if p.Name == profileName {
			f.Profiles[i].LastModel = model
			return true
		}
	}
	return false
}

// Find 按名字查找 profile
func (f *File) Find(name string) (Profile, bool) {
	for _, p := range f.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}
