package agentsvc

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"baize/internal/config"
	"baize/internal/mcp"
	"baize/internal/plugins"
)

// 本文件是「插件市场」在服务层的接线：把 plugins.Manager 需要的那几个口子（技能目录、
// MCP 配置读写、重扫技能）接到 Service 上，并给控制台/桌面端提供一组方法。
//
// 为什么钩子里要回调 s 而不是把 s 传进 plugins 包：plugins 只管"插件怎么装"，
// 白泽的配置长什么样是 agentsvc 的事——这样 plugins 包可以独立测试（见 plugins 包单测）。

// newPluginManager 建插件管理（在 New 里调用）
func (s *Service) newPluginManager(dataDir string) *plugins.Manager {
	return plugins.New(filepath.Join(dataDir, "plugins"), plugins.Hooks{
		SkillsDir: func() string {
			s.mu.RLock()
			lib := s.skills
			s.mu.RUnlock()
			if lib == nil {
				return ""
			}
			return lib.Dir()
		},
		// 技能目录变了要让技能库原地重扫——否则插件带的技能要等重启才看得见
		ReloadSkills: func() error {
			s.mu.RLock()
			lib := s.skills
			s.mu.RUnlock()
			if lib == nil {
				return errors.New("技能库未初始化")
			}
			return lib.Reload()
		},
		MCP: func() []mcp.ServerConfig {
			return append([]mcp.ServerConfig{}, s.Config().MCPServers...)
		},
		// MCPSave 会写回 config.json 并热插拔（连上新的、断开删掉的），语义正好是插件要的
		SaveMCP: func(cfgs []mcp.ServerConfig) error {
			_, err := s.MCPSave(cfgs)
			return err
		},
		AllowRemote: func() bool { return s.Config().AllowRemote },
	})
}

// Plugins 插件管理（控制台/桌面端用）
func (s *Service) Plugins() *plugins.Manager { return s.plugins }

// PluginSources 当前配置的插件源
func (s *Service) PluginSources() []config.PluginSource {
	return append([]config.PluginSource{}, s.Config().PluginSources...)
}

// pluginSources 把配置里的源转成 plugins 包认识的形状
func (s *Service) pluginSources() []plugins.Source {
	cfg := s.PluginSources()
	out := make([]plugins.Source, 0, len(cfg))
	for _, c := range cfg {
		out = append(out, plugins.Source{Name: c.Name, URL: c.URL, Enabled: c.Enabled})
	}
	return out
}

// PluginCatalog 逛市场：拉所有源的货架 + 已安装清单。
// 单个源拉不到不影响别的源（错误挂在那个源自己身上，见 plugins.Catalog）。
func (s *Service) PluginCatalog(ctx context.Context) plugins.Catalog {
	if s.plugins == nil {
		return plugins.Catalog{Sources: []plugins.SourceView{}, Available: []plugins.Available{}, Installed: []plugins.Installed{}}
	}
	return s.plugins.Catalog(ctx, s.pluginSources())
}

// PluginInstall 装一个插件：给 ID（从源里装）或 URL/本地路径（直接装）。
func (s *Service) PluginInstall(ctx context.Context, req plugins.InstallRequest) (plugins.Installed, error) {
	if s.plugins == nil {
		return plugins.Installed{}, errors.New("插件市场未启用")
	}
	if strings.TrimSpace(req.ID) != "" && len(req.Sources) == 0 {
		req.Sources = s.pluginSources()
	}
	return s.plugins.Install(ctx, req)
}

// PluginUninstall 卸载（技能与包一起归档，能捞回来）
func (s *Service) PluginUninstall(id string) (plugins.Installed, error) {
	if s.plugins == nil {
		return plugins.Installed{}, errors.New("插件市场未启用")
	}
	return s.plugins.Uninstall(id)
}

// PluginSetEnabled 停用 / 启用一个插件
func (s *Service) PluginSetEnabled(id string, on bool) error {
	if s.plugins == nil {
		return errors.New("插件市场未启用")
	}
	return s.plugins.SetEnabled(id, on)
}

// PluginAddSource 加一个插件源（按 url 去重：同一个地址重复加只会更新名字/启用状态）
func (s *Service) PluginAddSource(name, url string, enabled *bool) ([]config.PluginSource, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, errors.New("插件源的地址不能为空（给 index.json 的 http(s) 地址或本机路径）")
	}
	cfg := s.Config()
	on := true
	if enabled != nil {
		on = *enabled
	}
	name = strings.TrimSpace(name)
	found := false
	for i := range cfg.PluginSources {
		if strings.TrimSpace(cfg.PluginSources[i].URL) == url {
			if name != "" {
				cfg.PluginSources[i].Name = name
			}
			cfg.PluginSources[i].Enabled = on
			found = true
			break
		}
	}
	if !found {
		n := name
		if n == "" {
			n = url
		}
		cfg.PluginSources = append(cfg.PluginSources, config.PluginSource{Name: n, URL: url, Enabled: on})
	}
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	return s.PluginSources(), nil
}

// PluginRemoveSource 删一个插件源（按 url）
func (s *Service) PluginRemoveSource(url string) ([]config.PluginSource, error) {
	url = strings.TrimSpace(url)
	cfg := s.Config()
	kept := make([]config.PluginSource, 0, len(cfg.PluginSources))
	hit := false
	for _, src := range cfg.PluginSources {
		if strings.TrimSpace(src.URL) == url {
			hit = true
			continue
		}
		kept = append(kept, src)
	}
	if !hit {
		return nil, errors.New("没有这个插件源：" + url)
	}
	cfg.PluginSources = kept
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	return s.PluginSources(), nil
}
