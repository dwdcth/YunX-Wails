// ==UserScript==
// @name         电影天堂解析 (dytt8899.com)
// @namespace    yunx/plugins
// @version      0.1.0
// @description  解析电影天堂详情页（如 https://www.dytt8899.com/i/119283.html）：提取片名与 magnet/ftp/ed2k/torrent 下载链接，供普通下载引擎使用。
// @match        *://www.dytt8899.com/i/*
// @match        *://*.dytt8899.com/i/*
// @run-at       document-idle
// @grant        none
// ==/UserScript==

// YunX 插件契约：实现全局函数 yunx_parse(url)，返回
//   { ok: boolean, title?: string, links: [{url, name?, size?}], error?: string }
// 宿主 API：yunx_fetch(url) → {status, headers, body}（body 已转 UTF-8，GBK 站点直读）；
// 拉取成功后全局 document 自动指向该页面（类油猴语义），也可 yunx_dom(html) 显式解析；
// yunx_log(...) 打日志；GM_log/GM_xmlhttpRequest/console.log 兼容可用。

function yunx_parse(url) {
  var res = yunx_fetch(url);
  if (!res || res.status !== 200) {
    return { ok: false, error: '页面拉取失败: HTTP ' + (res ? res.status : '?') + (res && res.error ? ' (' + res.error + ')' : '') };
  }
  if (!res.body || res.body.length < 200) {
    return { ok: false, error: '页面内容为空（站点反爬或已改版？）' };
  }

  // 片名：<title>【寻秦记2025】迅雷下载_电影天堂</title> → 取【】内；无【】时剥掉
  // 「迅雷下载_电影天堂」尾巴后整段用。
  var rawTitle = (document.title || '').trim();
  var title = rawTitle;
  var m = rawTitle.match(/【(.+?)】/);
  if (m) {
    title = m[1];
  } else {
    title = rawTitle.replace(/迅雷下载.*$/, '').replace(/[_-]\s*电影天堂.*$/, '').trim();
  }

  // 收集链接：优先内容区 #Zoom，查不到（改版）则全页兜底——两处查全即可覆盖
  // 「最新资源列表」这类站内导航链接不会被收集（协议过滤挡掉）。
  var scope = document.querySelector('#Zoom') || document.querySelector('#downlist') || document.body;
  var anchors = scope.querySelectorAll('a[href]');
  if (anchors.length === 0) anchors = document.body.querySelectorAll('a[href]');

  var links = [];
  var seen = {};
  for (var i = 0; i < anchors.length; i++) {
    var a = anchors[i];
    var href = a.attr('href') || '';

    // 本站专属下载器协议 jianpian://pathtype=url&path=<真实链接>：剥壳取 path=。
    if (href.indexOf('jianpian://') === 0) {
      var at = href.indexOf('path=');
      if (at >= 0) {
        href = href.substring(at + 5);
        try { href = decodeURIComponent(href); } catch (e) { /* 原样使用 */ }
      }
    }

    var isMagnet = /^magnet:/i.test(href);
    var isFTP = /^ftp:\/\//i.test(href);
    var isEd2k = /^ed2k:/i.test(href);
    var isTorrent = /\.torrent(\?|#|$)/i.test(href);
    if (!isMagnet && !isFTP && !isEd2k && !isTorrent) continue; // http 站内页/广告链接全挡

    if (seen[href]) continue;
    seen[href] = true;

    var name = (a.text || '').trim();
    // magnet 的 dn= 参数常比锚文本更全（带压制组/清晰度信息）
    if (isMagnet) {
      var dn = href.match(/[?&]dn=([^&]+)/);
      if (dn) { try { name = decodeURIComponent(dn[1]) || name; } catch (e) {} }
    }
    if (isTorrent && !name) name = decodeURIComponent(href.split('/').pop().split('?')[0]);

    links.push({ url: href, name: name });
  }

  if (links.length === 0) {
    return { ok: false, error: '未找到下载链接（页面结构可能已改版）', title: title };
  }
  return { ok: true, title: title, links: links };
}
