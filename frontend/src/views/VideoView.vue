<script setup lang="ts">
// 视频解析页：对齐原版 VideoResolveScreen + VideoResultSection。
// video_parse 解析短视频/视频站链接（B 站/小红书/快手/微博/AcFun/Pixiv），
// B 站等多档位平台点清晰度 chip 走 video_parse_quality 切档；下载弹模式选择（合并/仅音频/仅视频）。
// 解析页与剪贴板建议条会把视频链接分流到本页（localStorage yunx.pendingVideo 接力自动解析）。
import { computed, onMounted, ref } from 'vue'
import {
  accountRemove, fmtBytes, videoAccountsList, videoDownload, videoLoginCookie, videoParse,
  type VideoAccount, type VideoMedia, type VideoParseResult,
} from '../core'
import AppIcon from '../components/AppIcon.vue'

const url = ref('')
const loading = ref(false)
const error = ref('')
const result = ref<VideoParseResult | null>(null)
const toast = ref('')

// 当前清晰度（全新解析清空，切档后置为所选 id；下载入队时随 video_download 回传）
const activeQuality = ref('')
// 正在切档的清晰度 id（chip 上做进行中提示）
const switching = ref('')

// 长文案默认收起，点「展开正文」翻全
const contentOpen = ref(false)

const videoMedias = computed(() => result.value?.medias.filter((m) => m.type === 'video') ?? [])
const imageMedias = computed(() => result.value?.medias.filter((m) => m.type === 'image') ?? [])

function flash(msg: string) {
  toast.value = msg
  setTimeout(() => (toast.value = ''), 2600)
}

/** 解析/切档：qualityId 为空 = 全新解析，否则 video_parse_quality 只换清晰度（结果整体替换） */
async function parse(text?: string, qualityId = '') {
  const u = (text ?? url.value).trim()
  if (!u || loading.value) return
  url.value = u
  loading.value = true
  error.value = ''
  switching.value = qualityId
  try {
    const r = await videoParse(u, qualityId)
    if (!r.ok) {
      error.value = r.error ?? '解析失败'
      if (!qualityId) result.value = null
      return
    }
    result.value = r
    activeQuality.value = qualityId
    contentOpen.value = false
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
    switching.value = ''
  }
}

// ---- 下载模式弹窗（.mask/.dlg 模式，对齐 DirPickerModal）----

const modeTarget = ref<VideoMedia | null>(null)
const modeBusy = ref(false)
const modeError = ref('')

function askDownload(m: VideoMedia) {
  modeError.value = ''
  modeTarget.value = m
}

/** mode = merge 合并 / audio 仅音频 / video 仅视频；B 站切档后的清晰度随 qualityId 回传 */
async function confirmDownload(mode: string) {
  const m = modeTarget.value
  if (!m || modeBusy.value) return
  modeBusy.value = true
  modeError.value = ''
  try {
    const r = await videoDownload(url.value, activeQuality.value, '', mode)
    if (r.ok) {
      modeTarget.value = null
      flash(`「${r.fileName || m.label}」已加入下载（任务 #${r.taskId}），进度见下载页`)
    } else {
      modeError.value = r.error ?? '下载失败'
    }
  } catch (e) {
    modeError.value = String(e)
  } finally {
    modeBusy.value = false
  }
}

// ---- 图片媒体：单张直链入队（不弹模式选择）----

const imgBusy = ref('') // 正在入队的图片 url

async function downloadImage(m: VideoMedia) {
  if (imgBusy.value) return
  imgBusy.value = m.url
  try {
    const r = await videoDownload(m.url)
    if (r.ok) flash(`图片已加入下载（任务 #${r.taskId}），进度见下载页`)
    else error.value = r.error ?? '下载失败'
  } catch (e) {
    error.value = String(e)
  } finally {
    imgBusy.value = ''
  }
}

// 解析页分流的接力：取 pendingVideo 自动填链并解析
async function checkPendingVideo() {
  const pending = localStorage.getItem('yunx.pendingVideo')
  if (!pending) return
  localStorage.removeItem('yunx.pendingVideo')
  parse(pending)
}

// ---- 视频账号管理（自设置页迁入：粘贴 Cookie 登录 + 账号列表删除 + 无需登录标注）----

// 游客通常可解析的平台（清晰度/图集可能受限）；其余建议登录后使用
const NO_LOGIN_PLATFORMS = ['bilibili', 'kuaishou', 'weibo', 'acfun']
const NO_LOGIN_HINT = 'B 站 / 快手 / 微博 / AcFun 通常无需登录即可解析；小红书 / Pixiv 建议粘贴 Cookie 登录'

const vPlatform = ref('')
const vCookie = ref('')
const vBusy = ref(false)
const vMsg = ref('')
const vAccounts = ref<VideoAccount[]>([])

async function refreshAccounts() {
  try { vAccounts.value = await videoAccountsList() } catch { /* 接口未就绪时静默 */ }
}

async function doVideoLogin() {
  const p = vPlatform.value.trim()
  const c = vCookie.value.trim()
  if (!p || !c || vBusy.value) return
  vBusy.value = true
  vMsg.value = '校验中…'
  try {
    const r = await videoLoginCookie(p, c)
    if (r.ok && r.loggedIn) {
      vMsg.value = '登录成功 ✓'
      vCookie.value = ''
      await refreshAccounts()
    } else {
      vMsg.value = r.error ?? '凭据校验失败'
    }
  } catch (e) {
    vMsg.value = String(e)
  } finally {
    vBusy.value = false
  }
}

async function removeAccount(a: VideoAccount) {
  if (!confirm(`移除 ${a.platform} 的登录态「${a.remark || a.nickname || a.id}」？`)) return
  try {
    await accountRemove(a.platform, a.id)
    await refreshAccounts()
    flash('已移除登录态')
  } catch (e) {
    flash(`移除失败：${e}`)
  }
}

onMounted(() => {
  checkPendingVideo()
  refreshAccounts()
})
</script>

<template>
  <div class="page">
    <!-- 视频账号管理（自设置页迁入）：登录态列表 + 粘贴 Cookie 登录 + 删除 -->
    <div class="card">
      <h3>视频账号<span class="tag-m6">登录态管理</span></h3>
      <p class="hint">{{ NO_LOGIN_HINT }}；已登录账号按平台保存，可随时移除</p>
      <div class="row" style="margin-bottom: 10px">
        <div class="field" style="flex: none; width: 180px">
          <label>平台标识（如 douyin / bilibili）</label>
          <input v-model="vPlatform" class="input" placeholder="bilibili" />
        </div>
        <div class="field" style="flex: 1; min-width: 200px">
          <label>登录凭据（Cookie）</label>
          <input v-model="vCookie" class="input" placeholder="粘贴 Cookie 串"
                 @keydown.enter="doVideoLogin" />
        </div>
        <button class="btn tonal small" style="align-self: flex-end"
                :disabled="!vPlatform.trim() || !vCookie.trim() || vBusy" @click="doVideoLogin">
          {{ vBusy ? '校验中…' : '登录' }}
        </button>
      </div>
      <div v-if="vMsg" class="banner" style="margin-bottom: 10px">{{ vMsg }}</div>
      <div v-if="vAccounts.length">
        <div v-for="a in vAccounts" :key="a.platform + a.id" class="acctrow">
          <span class="chip">{{ a.platform }}</span>
          <span class="acctname" :title="a.remark || a.nickname || a.id">
            {{ a.remark || a.nickname || a.id }}
          </span>
          <span v-if="a.isPrimary" class="chip plain">主账号</span>
          <span v-if="NO_LOGIN_PLATFORMS.includes(a.platform)" class="chip plain">该平台无需登录</span>
          <button class="btn text small" style="color: var(--error); margin-left: auto"
                  @click="removeAccount(a)">移除</button>
        </div>
      </div>
      <div v-else class="hint" style="margin: 0">暂无已登录视频平台账号</div>
    </div>

    <div class="card">
      <h3>解析视频链接</h3>
      <p class="hint">支持 B 站 / 小红书 / 快手 / 微博 / AcFun / Pixiv 的视频与图集，B 站可切换清晰度；下载经内置引擎入队</p>
      <div class="row">
        <div class="field" style="flex: 1; min-width: 260px">
          <label>视频链接</label>
          <input v-model="url" class="input" placeholder="https://www.bilibili.com/video/BV..."
                 @keydown.enter="parse()" />
        </div>
        <button class="btn" style="align-self: flex-end" :disabled="!url.trim() || loading" @click="parse()">
          {{ loading ? '解析中…' : '解析' }}
        </button>
      </div>
    </div>

    <div v-if="toast" class="banner">{{ toast }}</div>
    <div v-if="error" class="banner error">{{ error }}</div>

    <!-- 结果区（对齐 VideoResultSection） -->
    <div v-if="result" class="card">
      <div class="row" style="justify-content: space-between; margin-bottom: 6px">
        <div class="row" style="gap: 8px; min-width: 0">
          <span class="chip">{{ result.platform }}</span>
          <b class="vtitle" :title="result.title">{{ result.title }}</b>
        </div>
        <button class="btn text small" style="flex: none" @click="result = null">关闭</button>
      </div>
      <div class="vmeta">
        <span v-if="result.author">{{ result.author }}</span>
        <span v-if="result.publishTime">发布于 {{ result.publishTime }}</span>
      </div>
      <!-- 防盗链封面：no-referrer + 懒加载 -->
      <img v-if="result.cover" :src="result.cover" class="cover" alt="封面"
           referrerpolicy="no-referrer" loading="lazy" />
      <template v-if="result.content">
        <button class="btn text small" style="padding-left: 0" @click="contentOpen = !contentOpen">
          {{ contentOpen ? '收起正文' : '展开正文' }}
        </button>
        <p class="vcontent" :class="{ open: contentOpen }">{{ result.content }}</p>
      </template>

      <!-- 清晰度切档 chips -->
      <div v-if="result.qualities.length > 0" class="row" style="margin-top: 12px; gap: 8px">
        <span class="qlabel">清晰度</span>
        <button v-for="q in result.qualities" :key="q.id" class="chip qchip"
                :class="{ picked: activeQuality === q.id, switching: switching === q.id }"
                :disabled="loading" :title="`切换到 ${q.label}`" @click="parse(url, q.id)">
          {{ q.label }}
        </button>
      </div>

      <!-- 视频媒体行：清晰度 label + 大小 + 下载（弹模式选择） -->
      <div v-for="(m, i) in videoMedias" :key="'v' + i" class="filerow">
        <span class="ficon"><AppIcon name="play" /></span>
        <span class="fname">
          {{ m.label || '视频' }}
          <span v-if="m.isHls" class="chip plain" style="margin-left: 6px">HLS</span>
        </span>
        <span class="fmeta">{{ fmtBytes(m.size) }}</span>
        <button class="btn tonal small" @click="askDownload(m)">下载</button>
      </div>

      <!-- 图片媒体：缩略图网格（防盗链），单张下载 -->
      <div v-if="imageMedias.length" class="imggrid">
        <div v-for="(m, i) in imageMedias" :key="'i' + i" class="imgcell">
          <img :src="m.url" referrerpolicy="no-referrer" loading="lazy" alt="" />
          <button class="imgdl" :disabled="imgBusy === m.url"
                  :title="`下载第 ${i + 1} 张`" @click="downloadImage(m)">
            {{ imgBusy === m.url ? '…' : '⬇' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 下载模式选择：合并 / 仅音频 / 仅视频 -->
    <div v-if="modeTarget" class="mask" @click.self="modeTarget = null">
      <div class="dlg">
        <div class="row" style="justify-content: space-between; margin-bottom: 4px">
          <h3 style="margin: 0">选择下载模式</h3>
          <button class="btn text small" @click="modeTarget = null">关闭</button>
        </div>
        <p class="hint" style="margin-bottom: 6px">{{ modeTarget.label || '视频' }} · {{ fmtBytes(modeTarget.size) }}</p>
        <div class="modeitem" @click="confirmDownload('merge')">
          <b>合并下载</b><span>音视频合成为一个文件（推荐）</span>
        </div>
        <div class="modeitem" @click="confirmDownload('audio')">
          <b>仅音频</b><span>只下载音轨</span>
        </div>
        <div class="modeitem" @click="confirmDownload('video')">
          <b>仅视频</b><span>只下载画面（无音轨）</span>
        </div>
        <div v-if="modeError" class="banner error" style="margin-top: 10px">{{ modeError }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 视频账号列表行（自设置页迁入的账号卡） */
.acctrow { display: flex; align-items: center; gap: 8px; padding: 7px 0;
  border-bottom: 1px solid var(--outline-variant); }
.acctrow:last-child { border-bottom: none; }
.acctname { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; }
.vtitle { font-size: 15px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.vmeta { display: flex; gap: 12px; color: var(--on-bg-variant); font-size: 12.5px; margin: 0 0 10px; }
.cover { width: 100%; max-height: 340px; object-fit: cover; display: block;
  border-radius: var(--radius); border: 1px solid var(--outline-variant); margin-bottom: 10px; }
.vcontent { margin: 6px 0 0; color: var(--on-bg-variant); font-size: 13.5px;
  white-space: pre-wrap; word-break: break-word;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.vcontent.open { display: block; -webkit-line-clamp: none; overflow: visible; }
.qlabel { font-size: 13px; color: var(--on-bg-variant); }
.qchip { border: none; cursor: pointer; font: inherit; font-size: 12.5px; }
.qchip:disabled { opacity: 0.6; cursor: default; }
.qchip.switching { opacity: 0.6; }
.qchip.picked { outline: 2px solid var(--primary); outline-offset: 1px; }
.filerow { display: flex; align-items: center; gap: 10px; padding: 8px 2px; margin-top: 4px;
  border-bottom: 1px solid var(--outline-variant); }
.filerow:last-of-type { border-bottom: none; }
.ficon { color: var(--on-bg-variant); display: flex; }
.fname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fmeta { color: var(--on-bg-variant); font-size: 12.5px; flex: none; min-width: 64px; text-align: right; }
.imggrid { display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr)); gap: 10px; margin-top: 12px; }
.imgcell { position: relative; }
.imgcell img { width: 100%; aspect-ratio: 1; object-fit: cover; display: block;
  border-radius: 10px; border: 1px solid var(--outline-variant); }
.imgdl { position: absolute; right: 6px; bottom: 6px; width: 28px; height: 28px; padding: 0;
  border: none; border-radius: 50%; background: rgba(0, 0, 0, 0.55); color: #fff; cursor: pointer;
  display: flex; align-items: center; justify-content: center; font-size: 14px; }
.imgdl:disabled { opacity: 0.6; cursor: default; }
.mask { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.42); display: flex;
  align-items: center; justify-content: center; z-index: 40; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(420px, 92vw); border: 1px solid var(--outline-variant); }
.modeitem { display: flex; flex-direction: column; gap: 2px; padding: 12px 14px; margin-top: 8px;
  border: 1px solid var(--outline-variant); border-radius: var(--radius); cursor: pointer; }
.modeitem:hover { background: var(--surface-container); border-color: var(--primary); }
.modeitem b { font-size: 14px; }
.modeitem span { font-size: 12.5px; color: var(--on-bg-variant); }
</style>
