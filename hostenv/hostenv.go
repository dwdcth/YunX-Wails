// Package hostenv 提供 Go 壳侧的宿主环境探测与注入策略：
// 代理（系统/手动/直连）、外部 ffmpeg 路径、机器标识。
// 决策原则：探测在 Go 做，核心只收结果（host_set_* 命令，见 dll 模块 DllFacade）。
package hostenv

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// ResolveSystemProxy 探测系统代理：环境变量（https_proxy/http_proxy/all_proxy）优先，
// 其次 gsettings（GNOME 系桌面）。都探测不到返回空串 = 直连。
// 仅支持 http/https 型代理（核心 OkHttp 注入的是 java.net.Proxy.Type.HTTP；socks 返回空）。
// Windows 真机版应改为读注册表 Internet Settings（与原版 SystemProxy 语义对齐）。
func ResolveSystemProxy() (host string, port int) {
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if h, p, ok := parseProxy(v); ok {
				return h, p
			}
		}
	}
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("gsettings", "get", "org.gnome.system.proxy", "mode").Output(); err == nil &&
			strings.TrimSpace(string(out)) == "'manual'" {
			h := gsettingsGet("org.gnome.system.proxy.http", "host")
			p := gsettingsGet("org.gnome.system.proxy.http", "port")
			if host := strings.Trim(h, "'"); host != "" {
				if port, err := strconv.Atoi(strings.Trim(p, "'")); err == nil && port > 0 {
					return host, port
				}
			}
		}
	}
	return "", 0
}

// MachineId 返回用于 DbKeyStore 普通档密钥解包的机器标识，来源与各平台原版完全一致：
//   Windows：HKLM\SOFTWARE\Microsoft\Cryptography 的 MachineGuid；
//   Linux：/etc/machine-id（systemd），退回 /var/lib/dbus/machine-id（与 Linux 移植版同源，
//          已实测可解开其创建的 credential.key，指纹公式 "yunx-desktop|os|arch|id" 一致）。
// macOS 两者皆无，返回空——核心走「暂时读不到、下次重试」安全路径，绝不轮换密钥。
// ★ 注入的值必须与建钥来源一致；跨机器迁移的数据由用户经 kv settings.machineId 显式配置原机标识。
func MachineId() string {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("reg", "query",
			`HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid").Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(out), "\n") {
			if i := strings.Index(line, "REG_SZ"); i >= 0 {
				if v := strings.TrimSpace(line[i+len("REG_SZ"):]); v != "" {
					return v
				}
			}
		}
		return ""
	}
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				return v
			}
		}
	}
	return ""
}

// FfmpegAuto 在 PATH 里找系统 ffmpeg；找不到返回空串（核心回退到内置探测顺序）。
func FfmpegAuto() string {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ""
	}
	return p
}

func parseProxy(v string) (string, int, bool) {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "socks") {
		return "", 0, false
	}
	hostPort := v
	if i := strings.Index(v, "://"); i >= 0 {
		hostPort = v[i+3:]
	}
	host, portStr, ok := strings.Cut(hostPort, ":")
	host = strings.Trim(host, "[]") // IPv6 字面量
	port, err := strconv.Atoi(strings.Split(portStr, "/")[0])
	if !ok || host == "" || err != nil || port <= 0 {
		return "", 0, false
	}
	return host, port, true
}

func gsettingsGet(schema, key string) string {
	out, err := exec.Command("gsettings", "get", schema, key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
