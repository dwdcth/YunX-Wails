# YunX 插件开发指南（类油猴 .user.js）

插件 = 一个 `.user.js` 文件。放进插件目录即生效（免重启）。解析页粘贴命中
`@match` 的网址时自动调用，提取片名与下载链接交给下载引擎。

- 插件目录：`~/.yunx-wails/plugins/`（数据目录下；设置页可查看/安装/禁用/删除）
- 参考实现：[dytt8899.user.js](dytt8899.user.js)（电影天堂，约 80 行，含 GBK 站点、
  jianpian:// 协议剥壳、magnet dn 名提取等常见套路）

## 最小模板

```js
// ==UserScript==
// @name         我的站点解析
// @namespace    yunx/plugins
// @version      0.1.0
// @description  一句话说明
// @match        *://www.example.com/detail/*
// ==/UserScript==

function yunx_parse(url) {
  var res = yunx_fetch(url);            // 拉页面（body 已转 UTF-8，GBK 站点直读）
  if (!res || res.status !== 200) {
    return { ok: false, error: '页面拉取失败: HTTP ' + (res ? res.status : '?') };
  }
  // 拉取成功后全局 document 已指向该页面（类油猴语义），直接用 DOM API
  var title = (document.querySelector('h1') || {}).text || document.title;

  var links = [];
  var anchors = document.querySelectorAll('a[href]');
  for (var i = 0; i < anchors.length; i++) {
    var a = anchors[i];
    var href = a.attr('href') || '';
    if (/^(magnet:|ftp:\/\/|ed2k:)/i.test(href) || /\.torrent(\?|#|$)/i.test(href)) {
      links.push({ url: href, name: (a.text || '').trim() });
    }
  }
  if (links.length === 0) return { ok: false, error: '未找到下载链接', title: title };
  return { ok: true, title: title, links: links };
}
```

## 契约

**返回值**（yunx_parse 的返回，JS 对象或 JSON 字符串皆可）：

```jsonc
{
  "ok": true,                 // 必填；false 时给 error
  "title": "页面标题/片名",     // 可选，结果卡展示
  "links": [                  // 至少 1 条（ok=true 时）
    { "url": "magnet:?xt=…",  // magnet/ftp/ed2k/https…torrent 之外的会被丢弃
      "name": "显示名",        // 可选
      "size": "1.2GB" }       // 可选（展示用，不参与逻辑）
  ],
  "error": "失败原因"          // ok=false 时
}
```

链接类型与下载引擎的对应：`magnet:` → 迅雷式选文件弹窗（元数据→选文件→下载）；
`ftp://` → 内置 FTP 引擎；`.torrent` 文件 URL / `ed2k:` → aria2-next。

**元数据块**（`==UserScript==`，缺一不可的两项）：

- `@name` 插件名（结果卡/设置页展示）
- `@match` URL 匹配模式，可多行；`*` 通配任意串，如 `*://*.example.com/detail/*`

## 宿主 API

| API | 说明 |
|-----|------|
| `yunx_fetch(url)` | `{status, headers, body}`；成功后全局 `document` 自动指向该页面 |
| `document` / `yunx_dom(html)` | goquery DOM 兼容子集：`querySelector(All)`、`attr()`、`text`、`children` 等（注意：取属性用 `a.attr('href')`，不是 `a.href`） |
| `yunx_log(...)` | 打日志到应用日志（带插件名前缀） |
| `GM_log` / `GM_xmlhttpRequest` / `console.log` | 类油猴兼容层（GM_xmlhttpRequest 为同步回调） |
| `location.href` | 当前解析目标 URL |

## 执行环境说明

- JS 沙箱（goja）：**无** `os`/`io`/`fetch`/`XMLHttpRequest`/`require`，脚本之间零共享
- 单次执行上限 30 秒（死循环自动掐断）
- 编码：`yunx_fetch` 的 body 已按页面声明转 UTF-8（GBK 站点直接读）
- 安装/更新：设置页「从 URL 安装 / 本地安装」，或直接把 `.user.js` 丢进插件目录
- 禁用：设置页开关（文件改名 `.off`）；删除内置插件后重启会恢复出厂版

## 调试

应用日志看 `[pluginsys] [插件id]` 前缀输出（`yunx_log`/`console.log` 都落这里）；
解析失败时错误信息直接显示在解析页结果区。
