// dom.go：DOM 兼容子集——goquery 解析 HTML，句柄化暴露给 JS，由 JS 预置层组装成
// 油猴脚本熟悉的 document/Element 形状（querySelector 链式取值）。
//
// 设计取舍：Go 侧只提供 7 个 __dom_* 原语（解析/查询/取文本/取HTML/取属性/取标签/取标题），
// null 语义、链式调用、数组化 NodeList 全部在 JS 预置层完成——JS 语义用 JS 表达最省事，
// 也避免在 Go 回调里重入 goja 虚拟机（goja 非可重入，回调里只能返回值不能碰 VM 状态）。
package pluginsys

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/dop251/goja"
)

// domCtx 每次 yunx_parse 执行期的句柄表：id → goquery.Selection。
// 每次执行新建（随 goja Runtime 一起丢弃），插件不能跨执行持有 DOM 对象。
type domCtx struct {
	handles map[int]*goquery.Selection
	next    int
}

func newDomCtx() *domCtx {
	return &domCtx{handles: make(map[int]*goquery.Selection)}
}

func (d *domCtx) put(sel *goquery.Selection) int {
	d.next++
	d.handles[d.next] = sel
	return d.next
}

func (d *domCtx) get(id int) (*goquery.Selection, bool) {
	s, ok := d.handles[id]
	return s, ok
}

// installDomPrimitives 往 goja Runtime 注入 __yunx_* 原语与 DOM 预置层，并返回全局 document。
// 之后每次 yunx_fetch 成功都会由预置层把全局 document 重指向新页面（类油猴语义：
// 油猴脚本里 document 就是当前页，这里把「最近一次成功拉取的页面」当当前页）。
func (p *parseRunner) installDomPrimitives() error {
	rt := p.rt
	dom := p.dom

	// __dom_parse(html) → docHandle：解析 HTML 注册文档句柄。
	rt.Set("__dom_parse", func(call goja.FunctionCall) goja.Value {
		htmlText := call.Argument(0).String()
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlText))
		if err != nil { // goquery 对畸形 HTML 也能恢复，这里基本走不到；防御性兜底
			p.logf("dom parse: %v", err)
			return goja.Undefined()
		}
		return rt.ToValue(dom.put(doc.Selection))
	})

	// __dom_query(scopeHandle, selector) → [elHandle...]（cascadia 语法错误时空数组，不 panic）。
	rt.Set("__dom_query", func(call goja.FunctionCall) goja.Value {
		scopeID := call.Argument(0).ToInteger()
		selector := call.Argument(1).String()
		scope, ok := dom.get(int(scopeID))
		if !ok {
			return rt.ToValue([]int{})
		}
		ids := []int{}
		scope.Find(selector).Each(func(_ int, s *goquery.Selection) {
			ids = append(ids, dom.put(s))
		})
		return rt.ToValue(ids)
	})

	// __dom_text(h) → 合并文本（对应 DOM textContent）。
	rt.Set("__dom_text", func(call goja.FunctionCall) goja.Value {
		if s, ok := dom.handle(call); ok {
			return rt.ToValue(s.Text())
		}
		return goja.Null()
	})

	// __dom_html(h) → 内层 HTML（对应 innerHTML）。
	rt.Set("__dom_html", func(call goja.FunctionCall) goja.Value {
		if s, ok := dom.handle(call); ok {
			out, err := s.Html()
			if err != nil {
				return goja.Null()
			}
			return rt.ToValue(out)
		}
		return goja.Null()
	})

	// __dom_attr(h, name) → 属性值 | null（DOM getAttribute 缺省返回 null，不是 undefined）。
	rt.Set("__dom_attr", func(call goja.FunctionCall) goja.Value {
		if s, ok := dom.handle(call); ok {
			name := call.Argument(1).String()
			v, exists := s.Attr(name)
			if !exists {
				return goja.Null()
			}
			return rt.ToValue(v)
		}
		return goja.Null()
	})

	// __dom_tag(h) → 小写标签名（HTML 解析器本身统一小写；tagName 大写化在 JS 层做）。
	rt.Set("__dom_tag", func(call goja.FunctionCall) goja.Value {
		if s, ok := dom.handle(call); ok && len(s.Nodes) > 0 {
			return rt.ToValue(s.Nodes[0].Data)
		}
		return goja.Null()
	})

	// __dom_title(docHandle) → <title> 文本。
	rt.Set("__dom_title", func(call goja.FunctionCall) goja.Value {
		if s, ok := dom.handle(call); ok {
			return rt.ToValue(s.Find("title").First().Text())
		}
		return rt.ToValue("")
	})

	// 预置层：document / Element / yunx_dom / document 自动跟随 fetch。
	if _, err := rt.RunString(domPrelude); err != nil {
		return fmt.Errorf("DOM 预置层加载失败: %w", err)
	}
	return nil
}

// handle 从 FunctionCall 首参取句柄查 Selection。
func (d *domCtx) handle(call goja.FunctionCall) (*goquery.Selection, bool) {
	return d.get(int(call.Argument(0).ToInteger()))
}

// domPrelude DOM 兼容层（JS）。暴露形状：
//
//	document.title / .body / .querySelector(sel) / .querySelectorAll(sel) / .getElementById(id)
//	Element: .text .textContent .innerHTML（属性）；.html() .attr(name) .getAttribute(name)
//	         .tag .tagName .id（属性）；.querySelector(sel) .querySelectorAll(sel)（链式）
//	yunx_dom(html) → 显式解析任意 HTML 串（分页/iframe 场景），返回同形状 Document
//
// 刻意不做：classList/style 写操作、事件、createElement（无渲染场景无意义，报告详述）。
const domPrelude = `
function __yunx_Element(h) { this.__h = h; }
Object.defineProperties(__yunx_Element.prototype, {
  text:       { get: function () { return __dom_text(this.__h); } },
  textContent:{ get: function () { return __dom_text(this.__h); } },
  innerHTML:  { get: function () { return __dom_html(this.__h); } },
  tag:        { get: function () { return __dom_tag(this.__h); } },
  tagName:    { get: function () { var t = __dom_tag(this.__h); return t ? String(t).toUpperCase() : t; } },
  id:         { get: function () { var v = __dom_attr(this.__h, 'id'); return v === null ? '' : v; } }
});
__yunx_Element.prototype.html = function () { return __dom_html(this.__h); };
__yunx_Element.prototype.attr = function (name) { return __dom_attr(this.__h, name); };
__yunx_Element.prototype.getAttribute = function (name) { return __dom_attr(this.__h, name); };
__yunx_Element.prototype.querySelector = function (sel) {
  var ids = __dom_query(this.__h, sel);
  return ids.length > 0 ? new __yunx_Element(ids[0]) : null;
};
__yunx_Element.prototype.querySelectorAll = function (sel) {
  return __dom_query(this.__h, sel).map(function (id) { return new __yunx_Element(id); });
};

function __yunx_Document(h) { this.__h = h; }
__yunx_Document.prototype = Object.create(__yunx_Element.prototype);
__yunx_Document.prototype.constructor = __yunx_Document;
Object.defineProperties(__yunx_Document.prototype, {
  title: { get: function () { return __dom_title(this.__h); } },
  body:  { get: function () { return this.querySelector('body') || new __yunx_Element(this.__h); } }
});
__yunx_Document.prototype.getElementById = function (id) {
  id = String(id);
  if (!/^[A-Za-z0-9_-]+$/.test(id)) return null;
  return this.querySelector('#' + id);
};

// 显式解析：yunx_dom(html) → Document（分页/二级页面场景）
function yunx_dom(html) { return new __yunx_Document(__dom_parse(String(html))); }

// 全局 document：空文档起步，每次 yunx_fetch 成功后自动重指向（油猴语义近似）。
var document = yunx_dom('<!DOCTYPE html><html><head><title></title></head><body></body></html>');
`
