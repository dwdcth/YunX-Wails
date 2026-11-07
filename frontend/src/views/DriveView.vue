<script setup lang="ts">
// 网盘页：账号卡片网格（配额）+ 点入平台后的云盘文件浏览器（列目录/建夹/重命名/移动/删除/下载/转存入口）。
import { computed, onMounted, ref } from 'vue'
import { CoreService } from '../../bindings/yunxwailsdemo/index.js'
import {
  accountRemove, accountSetPrimary, accountSetRemark, accountsList,
  cloudDelete, cloudDownload, cloudLink, cloudList, cloudMkdir, cloudMove, cloudQuota, cloudRename,
  isPreviewableVideo,
  cloudShare, fmtBytes, type AccountEntry, type CloudQuota, type ShareFile,
} from '../core'
import AppIcon from '../components/AppIcon.vue'
import DirPickerModal from '../components/DirPickerModal.vue'
import LoginModal from '../components/LoginModal.vue'

const PLATFORMS = [
  { key: 'quark', name: '夸克网盘', color: '#3B82F6', canDl: true, paste: 'Cookie（浏览器 F12 → 网络 → 任意请求的 Cookie 头）' },
  { key: 'uc', name: 'UC网盘', color: '#F97316', canDl: true, paste: 'Cookie' },
  { key: 'xunlei', name: '迅雷云盘', color: '#1E40AF', canDl: false, paste: '网页凭据串（原版网页登录导出的原始串）' },
  { key: 'baidu', name: '百度网盘', color: '#2563EB', canDl: true, paste: 'Cookie（需含 STOKEN/BDUSS）' },
  { key: 'c139', name: '移动云盘', color: '#0891C2', canDl: true, paste: 'Cookie（需含 Authorization）' },
  { key: 'pan123', name: '123云盘', color: '#7C3AED', canDl: true, paste: '' },
  { key: 'pan115', name: '115网盘', color: '#0D9488', canDl: true, paste: 'Cookie（需含 UID/CID/SEID）' },
  { key: 'guangya', name: '光鸭云盘', color: '#D97706', canDl: true, paste: '' },
  { key: 'ilanzou', name: '蓝奏云优享版', color: '#BE185D', canDl: true, paste: '' },
  { key: 'lanzou', name: '蓝奏云', color: '#DB2777', canDl: true, paste: 'Cookie（需含 ylogin 与 phpdisk_info）' },
]

// 支持账号密码登录的平台（与 LoginModal 的密码 tab 一致）
const PASSWORD_PLATFORMS = ['pan123', 'guangya', 'ilanzou', 'xunlei']

const accounts = ref<AccountEntry[]>([])
const quotas = ref<Record<string, CloudQuota | null>>({})
const error = ref('')
const expanded = ref<string | null>(null)
const remarkDraft = ref('')

// 浏览器模式状态
const browsing = ref<string | null>(null)
const files = ref<ShareFile[]>([])
// 大目录分块渲染：WebKit 一次性渲染数千行会触发绘制/合成问题（整屏空白），分批上屏
const renderLimit = ref(300)
const visibleFiles = computed(() => files.value.slice(0, renderLimit.value))

// WebKitGTK 中段绘制丢失缓解：列表大替换后强制重新合成（对齐 App.vue 启动时的 paintKick）
function paintKick() {
  requestAnimationFrame(() => requestAnimationFrame(() => {
    const el = document.documentElement
    el.style.transform = 'translateZ(0)'
    requestAnimationFrame(() => requestAnimationFrame(() => { el.style.transform = '' }))
  }))
}
const stack = ref<{ fid: string; name: string }[]>([])
const listLoading = ref(false)
const toast = ref('')
const mkdirName = ref('')
const showMkdir = ref(false)
const renaming = ref<ShareFile | null>(null)
const renameText = ref('')
const moving = ref<ShareFile | null>(null)
const loggingIn = ref<string | null>(null)

// 浏览器模式多选 + 创建分享（fids 与下载页同款 .ck 复选样式）
const selected = ref(new Set<string>())
const showShare = ref(false)
const shareTitle = ref('')
const sharePass = ref('')
const sharing = ref(false)
// 有效期下拉：expiredType 对齐原版 ShareExpire 常量（0 永久 / 1 天 / 2 周 / 3 月）
const SHARE_EXPIRES = [
  { v: 0, label: '永久有效' },
  { v: 1, label: '1 天' },
  { v: 2, label: '7 天' },
  { v: 3, label: '30 天' },
]
const shareExpire = ref(0)

const selFiles = computed(() => files.value.filter((f) => selected.value.has(f.fid)))

function toggleSel(fid: string) {
  if (selected.value.has(fid)) selected.value.delete(fid)
  else selected.value.add(fid)
}

function clearSel() {
  selected.value.clear()
  showShare.value = false
}

/** 全选三态（当前目录全部文件；分块渲染只影响展示，选中集合按整个 files 判定）：
 *  true=全选 / null=半选 / false=未选 */
const allFilesState = computed<boolean | null>(() => {
  const list = files.value
  if (list.length === 0) return false
  let n = 0
  for (const f of list) if (selected.value.has(f.fid)) n++
  return n === 0 ? false : n === list.length ? true : null
})

/** 全选开关：选中当前目录全部文件（文件列表不含「..」，上级导航走面包屑）；再点取消全选 */
function toggleSelectAll() {
  if (allFilesState.value === true) selected.value.clear()
  else for (const f of files.value) selected.value.add(f.fid)
}

const quotaOf = (key: string) => quotas.value[key]
const byPlatform = computed(() => {
  const m = new Map<string, AccountEntry[]>()
  for (const a of accounts.value) {
    const list = m.get(a.platform) ?? []
    list.push(a)
    m.set(a.platform, list)
  }
  return m
})
const platformMeta = computed(() => PLATFORMS.find((p) => p.key === browsing.value))

function flash(msg: string) {
  toast.value = msg
  setTimeout(() => (toast.value = ''), 2600)
}

async function refresh() {
  try {
    accounts.value = await accountsList()
    error.value = ''
  } catch (e) {
    error.value = String(e)
  }
}

async function refreshQuota() {
  try { quotas.value = await cloudQuota() } catch { /* 配额失败不打扰 */ }
}

async function act(fn: () => unknown) {
  try { await fn() } catch (e) { error.value = String(e) }
  await refresh()
}

function toggle(key: string) {
  if (expanded.value === key) {
    expanded.value = null
  } else {
    expanded.value = key
    const primary = byPlatform.value.get(key)?.find((a) => a.isPrimary)
    remarkDraft.value = primary?.remark ?? ''
  }
}

async function openDir(fid: string, name: string, replace = false) {
  if (!browsing.value || listLoading.value) return
  listLoading.value = true
  clearSel() // 换目录后旧选择失效
  try {
    const r = await cloudList(browsing.value, fid)
    if (r.ok && r.files) {
      files.value = r.files
      renderLimit.value = 300
      paintKick()
      if (replace) stack.value = [{ fid: r.dirFid, name: '根目录' }]
      else stack.value.push({ fid, name })
    } else {
      error.value = r.error ?? '列目录失败'
    }
  } finally {
    listLoading.value = false
  }
}

function enterPlatform(key: string) {
  browsing.value = key
  files.value = []
  renderLimit.value = 300
  stack.value = []
  clearSel()
  openDir('', '', true)
}

function backTo(level: number) {
  if (level >= stack.value.length - 1) return
  const target = stack.value[level]
  listLoading.value = true
  clearSel()
  cloudList(browsing.value!, target.fid).then((r) => {
    if (r.ok && r.files) {
      files.value = r.files
      stack.value = stack.value.slice(0, level + 1)
    }
  }).finally(() => (listLoading.value = false))
}

async function doMkdir() {
  if (!browsing.value || !mkdirName.value.trim()) return
  const r = await cloudMkdir(browsing.value, mkdirName.value.trim())
      .catch((e): { ok: boolean; error: string; shareUrl?: string } => ({ ok: false, error: String(e) }))
  if (r.ok) {
    showMkdir.value = false
    mkdirName.value = ''
    await reloadCurrent()
    flash('已创建')
  } else {
    flash(`创建失败：${r.error}`)
  }
}

async function reloadCurrent() {
  const cur = stack.value[stack.value.length - 1]
  if (!cur || !browsing.value) return
  const r = await cloudList(browsing.value, cur.fid).catch(() => null)
  if (r?.ok && r.files) {
    files.value = r.files
    renderLimit.value = 300
    paintKick()
  }
}

// ---- 云盘视频/音频在线预览（cloud_link 取直链 → <video> 原生播放）----

const previewUrl = ref('')
const previewName = ref('')
const previewBusy = ref(false)
const previewFail = ref('')

function closePreview() {
  previewUrl.value = ''
  previewFail.value = ''
}

async function doPreview(f: ShareFile) {
  if (!browsing.value || previewBusy.value) return
  previewBusy.value = true
  error.value = ''
  try {
    // 首选 ffplay 原生窗口（自带关闭/进度/音量，支持需鉴权头的平台）；没装才回退页内代理播放
    try {
      const play = JSON.parse(await CoreService.CloudPreviewPlay(
        browsing.value, f.fid, f.fname, f.fsize ?? 0, f.fidToken ?? ''))
      if (play.ok) { flash(`已用 ffplay 打开「${f.fname}」（独立窗口）`); return }
    } catch { /* 继续回退 */ }
    const r = JSON.parse(await CoreService.CloudPreviewURL(
      browsing.value, f.fid, f.fname, f.fsize ?? 0, f.fidToken ?? ''))
    if (r.ok && r.url) {
      previewUrl.value = r.url
      previewName.value = f.fname
      previewFail.value = ''
    } else {
      error.value = r.error ?? '取直链失败'
    }
  } catch (e) {
    error.value = String(e)
  } finally {
    previewBusy.value = false
  }
}

/** 多选创建分享：cloud_share → toast 显示 shareUrl 并尝试写剪贴板 */
async function doShare() {
  if (!browsing.value || !selFiles.value.length || sharing.value) return
  sharing.value = true
  const r = await cloudShare(browsing.value, selFiles.value.map((f) => f.fid),
                             shareTitle.value.trim(), sharePass.value.trim(), shareExpire.value)
      .catch((e): { ok: boolean; error: string; shareUrl?: string } => ({ ok: false, error: String(e) }))
  sharing.value = false
  if (r.ok && r.shareUrl) {
    // 复制失败（如非安全上下文）只影响剪贴板，链接仍展示在 toast
    navigator.clipboard?.writeText(r.shareUrl).catch(() => {})
    flash(`分享已创建并复制链接：${r.shareUrl}`)
    showShare.value = false
    shareTitle.value = ''
    sharePass.value = ''
    clearSel()
    await reloadCurrent()
  } else {
    flash(`创建分享失败：${r.error}`)
  }
}

async function doRename() {
  if (!browsing.value || !renaming.value) return
  const r = await cloudRename(browsing.value, renaming.value, renameText.value.trim())
      .catch((e): { ok: boolean; error: string; shareUrl?: string } => ({ ok: false, error: String(e) }))
  if (r.ok) {
    renaming.value = null
    await reloadCurrent()
    flash('已重命名')
  } else {
    flash(`重命名失败：${r.error}`)
  }
}

async function doDelete(f: ShareFile) {
  if (!browsing.value) return
  if (!confirm(`删除「${f.fname}」？`)) return
  const r = await cloudDelete(browsing.value, [f]).catch((e): { ok: boolean; error: string; shareUrl?: string } => ({ ok: false, error: String(e) }))
  if (r.ok) {
    await reloadCurrent()
    flash('已删除')
  } else {
    flash(`删除失败：${r.error}`)
  }
}

async function doDownload(f: ShareFile) {
  if (!browsing.value) return
  const r = await cloudDownload(browsing.value, f).catch((e): { ok: boolean; error: string; taskId?: number } => ({ ok: false, error: String(e) }))
  if (r.ok) {
    flash(`「${f.fname}」已加入下载（任务 #${r.taskId}）`)
  } else {
    flash(`下载失败：${r.error}`)
  }
}

async function doMove(targetFid: string, targetPath: string) {
  if (!browsing.value || !moving.value) return
  const r = await cloudMove(browsing.value, [moving.value], targetFid, targetPath)
      .catch((e): { ok: boolean; error: string; shareUrl?: string } => ({ ok: false, error: String(e) }))
  moving.value = null
  if (r.ok) {
    await reloadCurrent()
    flash('已移动')
  } else {
    flash(`移动失败：${r.error}`)
  }
}

function quotaPct(q: CloudQuota) {
  return q.total > 0 ? Math.min(100, Math.round((q.used / q.total) * 100)) : 0
}

onMounted(() => {
  refresh()
  refreshQuota()
})
</script>

<template>
  <div class="page">
    <div v-if="toast" class="banner">{{ toast }}</div>
    <div v-if="error" class="banner error">{{ error }}</div>

    <!-- 文件浏览器模式 -->
    <template v-if="browsing && platformMeta">
      <div class="card">
        <div class="row" style="justify-content: space-between; margin-bottom: 8px">
          <div class="row" style="gap: 8px">
            <button class="btn text small" @click="browsing = null">← 返回</button>
            <span class="chip">{{ platformMeta.name }}</span>
            <button class="btn text small" :disabled="listLoading" @click="reloadCurrent()">刷新</button>
            <button class="btn text small" @click="showMkdir = !showMkdir">新建文件夹</button>
            <!-- 全选：作用于当前目录全部文件；选中后「创建分享」等批量操作按已选集合生效 -->
            <span v-if="files.length > 0" class="selall" title="选中/取消选中当前目录全部文件"
                  @click="toggleSelectAll">
              <span class="ck" :class="{ on: allFilesState === true, half: allFilesState === null }">
                <AppIcon v-if="allFilesState === true" name="check" :size="13" />
              </span>
              全选
            </span>
            <button v-if="selected.size > 0" class="btn tonal small" @click="showShare = !showShare">
              创建分享（已选 {{ selected.size }}）
            </button>
          </div>
          <div v-if="quotaOf(browsing)" class="qbar">
            <span class="qlabel">{{ fmtBytes(quotaOf(browsing)!.used) }} / {{ fmtBytes(quotaOf(browsing)!.total) }}</span>
            <i class="qtrack"><b :style="{ width: quotaPct(quotaOf(browsing)!) + '%' }" /></i>
          </div>
        </div>
        <div v-if="showMkdir" class="row" style="margin-bottom: 10px">
          <input v-model="mkdirName" class="input" style="flex: 1" placeholder="新文件夹名称"
                 @keydown.enter="doMkdir" />
          <button class="btn tonal small" @click="doMkdir">创建</button>
        </div>
        <div v-if="showShare && selected.size > 0" class="row" style="margin-bottom: 10px">
          <input v-model="shareTitle" class="input" style="flex: 1; min-width: 140px"
                 placeholder="分享标题（可选）" @keydown.enter="doShare" />
          <input v-model="sharePass" class="input" style="flex: none; width: 120px"
                 placeholder="提取码（可选）" @keydown.enter="doShare" />
          <select v-model.number="shareExpire" class="input" style="flex: none; width: 110px"
                  title="分享有效期">
            <option v-for="o in SHARE_EXPIRES" :key="o.v" :value="o.v">{{ o.label }}</option>
          </select>
          <button class="btn small" :disabled="sharing" @click="doShare">
            {{ sharing ? '创建中…' : `分享 ${selected.size} 项` }}
          </button>
          <button class="btn text small" @click="showShare = false">取消</button>
        </div>
        <div class="row crumbs" style="margin: 4px 0 10px">
          <template v-for="(d, i) in stack" :key="d.fid + i">
            <button class="crumb" :disabled="i === stack.length - 1" @click="backTo(i)">{{ d.name }}</button>
            <span v-if="i < stack.length - 1" class="sep">/</span>
          </template>
        </div>

        <div v-if="listLoading" class="banner">加载目录…</div>
        <div v-else-if="files.length === 0" class="empty"><p>目录为空</p></div>

        <div v-for="f in visibleFiles" :key="f.fid" class="filerow">
          <span class="ck" :class="{ on: selected.has(f.fid) }" title="选中" @click="toggleSel(f.fid)">
            <AppIcon v-if="selected.has(f.fid)" name="check" :size="13" />
          </span>
          <span class="ficon"><AppIcon :name="f.isdir ? 'cloud' : 'file'" /></span>
          <template v-if="renaming?.fid === f.fid">
            <input v-model="renameText" class="input" style="flex: 1"
                   @keydown.enter="doRename" @keydown.esc="renaming = null" />
            <button class="btn tonal small" @click="doRename">保存</button>
            <button class="btn text small" @click="renaming = null">取消</button>
          </template>
          <template v-else>
            <span class="fname" @click="f.isdir && openDir(f.fid, f.fname)">{{ f.fname }}</span>
            <span class="fmeta">{{ f.isdir ? '目录' : fmtBytes(f.fsize) }}</span>
            <button v-if="!f.isdir && isPreviewableVideo(f.fname)" class="btn text small"
                    :disabled="previewBusy" title="在线播放" @click="doPreview(f)">预览</button>
            <button v-if="!f.isdir && platformMeta.canDl" class="btn tonal small" @click="doDownload(f)">下载</button>
            <button class="btn text small" @click="renaming = f; renameText = f.fname">重命名</button>
            <button class="btn text small" @click="moving = f">移动</button>
            <button class="btn text small" style="color: var(--error)" @click="doDelete(f)">删除</button>
          </template>
        </div>
      </div>
      <div v-if="visibleFiles.length < files.length" class="morebar">
        已显示 {{ visibleFiles.length }} / {{ files.length }} 条（WebKit 大列表分批渲染防画丢）
        <button class="btn tonal small" @click="renderLimit += 500">显示更多</button>
      </div>
      <DirPickerModal v-if="moving" :platform="browsing" title="移动到哪个文件夹？"
                      @confirm="doMove" @cancel="moving = null" />
    </template>

    <!-- 卡片网格模式 -->
    <template v-else>
      <div class="banner">账号/设置/任务与原版同一数据库互通；登录入口（WebView2/粘贴 Cookie）待批次 D。</div>
      <div class="grid">
        <div v-for="p in PLATFORMS" :key="p.key" class="cell" :class="{ clickable: byPlatform.get(p.key)?.length }"
             @click="byPlatform.get(p.key)?.length && enterPlatform(p.key)">
          <div class="name"><i class="dot" :style="{ background: p.color }" />{{ p.name }}</div>
          <template v-if="byPlatform.get(p.key)?.length">
            <div class="sub">已登录 {{ byPlatform.get(p.key)!.length }} 个账号 · 点击进入</div>
            <div class="sub" style="color: var(--on-bg)">
              当前：{{ byPlatform.get(p.key)!.find(a => a.isPrimary)?.remark
                || byPlatform.get(p.key)!.find(a => a.isPrimary)?.nickname || '—' }}
            </div>
            <div v-if="quotaOf(p.key)" class="qbar" @click.stop>
              <span class="qlabel">{{ fmtBytes(quotaOf(p.key)!.used) }} / {{ fmtBytes(quotaOf(p.key)!.total) }}</span>
              <i class="qtrack"><b :style="{ width: quotaPct(quotaOf(p.key)!) + '%' }" /></i>
            </div>
            <div class="row" style="margin-top: 4px" @click.stop>
              <button class="btn text small" @click="enterPlatform(p.key)">浏览文件</button>
              <button class="btn text small" @click="toggle(p.key)">
                {{ expanded === p.key ? '收起账号' : '管理账号' }}
              </button>
            </div>
            <div v-if="expanded === p.key" style="border-top: 1px solid var(--outline-variant); margin-top: 8px; padding-top: 8px">
              <div v-for="a in byPlatform.get(p.key)" :key="a.id"
                   style="padding: 6px 0; border-bottom: 1px dashed var(--outline-variant)">
                <div class="row" style="justify-content: space-between">
                  <span style="font-size: 13px">
                    {{ a.remark || a.nickname || a.id }}
                    <span v-if="a.isPrimary" class="chip" style="margin-left: 4px">主</span>
                  </span>
                  <span class="row" style="gap: 2px">
                    <button v-if="!a.isPrimary" class="btn text small" @click="act(() => accountSetPrimary(p.key, a.id))">设主</button>
                    <button class="btn text small" style="color: var(--error)" @click="act(() => accountRemove(p.key, a.id))">移除</button>
                  </span>
                </div>
              </div>
              <div class="row" style="margin-top: 8px">
                <input class="input" style="flex: 1; font-size: 13px" v-model="remarkDraft" placeholder="主账号备注" />
                <button class="btn tonal small" @click="act(() => {
                  const primary = byPlatform.get(p.key)?.find(a => a.isPrimary)
                  if (primary) accountSetRemark(p.key, primary.id, remarkDraft.trim())
                })">保存备注</button>
              </div>
            </div>
          </template>
          <template v-else>
            <div class="sub">未登录</div>
            <div class="row" style="margin-top: 2px" v-if="p.paste">
              <button class="btn tonal small" @click="loggingIn = p.key">粘贴凭据登录</button>
            </div>
            <div class="row" style="margin-top: 2px" v-else-if="PASSWORD_PLATFORMS.includes(p.key)">
              <button class="btn tonal small" @click="loggingIn = p.key">密码登录</button>
            </div>
            <div class="sub" style="opacity: 0.75" v-else>登录待后续批次</div>
          </template>
        </div>
      </div>
      <LoginModal v-if="loggingIn && PLATFORMS.find(p => p.key === loggingIn)"
                  :platform="loggingIn"
                  :platform-name="PLATFORMS.find(p => p.key === loggingIn)!.name"
                  :credential-hint="PLATFORMS.find(p => p.key === loggingIn)!.paste"
                  @done="(ok) => { loggingIn = null; if (ok) { refresh(); refreshQuota() } }"
                  @cancel="loggingIn = null" />
    </template>
  
    <!-- 云盘视频预览：Go 本地代理流（带核心请求头+Range），Esc/点背景/按钮可关 -->
    <div v-if="previewUrl" class="preview-mask" tabindex="0"
         @keydown.esc="previewUrl = ''" @click.self="closePreview">
      <div class="preview-box">
        <div class="preview-bar">
          <b>{{ previewName }}</b>
          <span v-if="previewFail" class="dim" style="font-size: 12px; color: var(--error)">{{ previewFail }}</span>
          <button class="btn text small" style="margin-left: auto" @click="closePreview">关闭 (Esc)</button>
        </div>
        <video :key="previewUrl" :src="previewUrl" controls autoplay
               style="width: 100%; height: calc(100% - 44px); background: #000"
               @error="previewFail = '该源无法播放（格式不支持或源站拒绝），可下载后本地观看'" />
      </div>
    </div>
</div>
</template>

<style scoped>
.cell.clickable { cursor: pointer; }
.cell.clickable:hover { border-color: var(--primary); }
.qbar { display: flex; align-items: center; gap: 8px; margin-top: 2px; }
.qlabel { font-size: 11.5px; color: var(--on-bg-variant); white-space: nowrap; }
.qtrack { flex: 1; height: 5px; border-radius: 3px; background: var(--surface-container-high); overflow: hidden; }
.qtrack b { display: block; height: 100%; background: var(--primary); }
.filerow { display: flex; align-items: center; gap: 8px; padding: 7px 2px;
  border-bottom: 1px solid var(--outline-variant); flex-wrap: wrap; }
.filerow:last-child { border-bottom: none; }
.ck { width: 18px; height: 18px; border-radius: 4px; border: 2px solid var(--outline);
  cursor: pointer; flex: none; display: inline-flex; align-items: center; justify-content: center;
  color: var(--on-primary); }
.ck.on { background: var(--primary); border-color: var(--primary); }
/* 半选态（indeterminate）：实底 + 居中横杠（AppIcon 无减号图标，纯 CSS 画） */
.ck.half { background: var(--primary); border-color: var(--primary); }
.ck.half::after { content: ''; width: 10px; height: 2px; border-radius: 1px; background: var(--on-primary); }
/* 工具栏「全选」控件：复选框 + 文案（与行复选共用 .ck，风格对齐下载页） */
.selall { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; flex: none;
  font-size: 13px; color: var(--on-bg-variant); user-select: none; }
.selall:hover { color: var(--on-bg); }
.selall:hover .ck { border-color: var(--primary); }
.ficon { color: var(--on-bg-variant); display: flex; }
.fname { flex: 1; min-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.filerow .fname { cursor: pointer; }
.fmeta { color: var(--on-bg-variant); font-size: 12.5px; flex: none; min-width: 56px; text-align: right; }
.crumbs { flex-wrap: wrap; }
.crumb { border: none; background: transparent; color: var(--primary); cursor: pointer;
  padding: 2px 6px; border-radius: 6px; font-size: 13px; }
.crumb:disabled { color: var(--on-bg); font-weight: 600; }
.sep { color: var(--outline); }
/* 云盘视频预览覆盖层 */
.preview-mask { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.6); display: flex;
  align-items: center; justify-content: center; z-index: 60; }
.preview-box { width: min(88vw, 1100px); height: min(78vh, 720px); display: flex;
  flex-direction: column; background: var(--surface, #1e1e1e); border-radius: 12px; overflow: hidden;
  box-shadow: 0 18px 60px rgba(0, 0, 0, 0.5); }
.preview-bar { display: flex; align-items: center; gap: 12px; padding: 8px 14px; height: 44px;
  box-sizing: border-box; }
.preview-bar b { flex: 1; font-size: 14px; }
.morebar { display: flex; align-items: center; gap: 12px; justify-content: center;
  padding: 10px; color: var(--on-bg-variant); font-size: 13px; }
</style>
