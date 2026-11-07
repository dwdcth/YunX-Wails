// pluginsys_host.go：插件系统服务方法（CoreService）——类油猴插件系统的前端入口。
//
// 返回值风格与 service.go/normaldl.go 一致：JSON 字符串（前端经 bindings 拿到后自行解析）。
// 插件目录：<数据目录>/plugins/*.user.js（数据目录与 downloadd 同源：YUNX_DESKTOP_DATA_DIR
// 或 ~/.yunx-wails）。惰性单例（normalDL 同款模式），无需 main.go 挂接，失败不阻断启动。
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"yunxwailsdemo/pluginsys"
)

// pluginSys 进程级插件管理器（惰性初始化；不依赖 main 启动时注入）。
var (
	pluginSysOnce sync.Once
	pluginSys     *pluginsys.Manager
)

// pluginSysReady 惰性获取插件管理器（数据目录同 downloadd：env > ~/.yunx-wails）。
func pluginSysReady() *pluginsys.Manager {
	pluginSysOnce.Do(func() {
		dataDir := os.Getenv("YUNX_DESKTOP_DATA_DIR")
		if dataDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dataDir = filepath.Join(home, ".yunx-wails")
			}
		}
		pluginSys = pluginsys.NewManager(dataDir)
		pluginSys.SetLogger(log.New(os.Stdout, "[pluginsys] ", log.LstdFlags|log.Lmsgprefix))
		fmt.Printf("插件系统就绪: 插件目录 %s\n", pluginSys.PluginDir())
	})
	return pluginSys
}

// PluginList 插件清单。返回 {ok, plugins:[{id,name,version,match,file}], warnings?}。
// 丢新 .user.js 进插件目录后再次调用即可见（免重启）。
func (s *CoreService) PluginList() (string, error) {
	m := pluginSysReady()
	plugins, problems := m.List()
	if plugins == nil {
		plugins = []*pluginsys.Plugin{}
	}
	out := map[string]any{"ok": true, "plugins": plugins}
	if len(problems) > 0 {
		out["warnings"] = problems
	}
	return marshal(out)
}

// PluginParse 用指定插件解析 url。返回插件契约 JSON：{ok, title?, links:[{url,name?,size?}], error?}。
// links 里的 url 由前端喂给 NormalDownloadSubmit（普通下载引擎）。
func (s *CoreService) PluginParse(pluginId, url string) (string, error) {
	m := pluginSysReady()
	res, err := m.Parse(pluginId, url)
	if err != nil {
		return errorJSON(err.Error()), nil
	}
	return res, nil
}
