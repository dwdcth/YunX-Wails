<script setup lang="ts">
// 设置页：全部经核心 settings_list/settings_set 读写**正式设置**（yunx/settings，
// 与 Compose 原版 / Linux 移植版同一份数据）；代理与 ffmpeg 由 Go 宿主探测/注入。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { CoreService } from '../../bindings/yunxwailsdemo/index.js'
import { githubMirrorRefresh, kvPut, settingsList, settingsSet, settingsSetPlatform, updateCheck,
         type AppSettings } from '../core'
import { applySeedColor, cachedSeedColor, seedNumToHex, setThemeMode, theme } from '../theme'

const router = useRouter()
const s = ref<AppSettings | null>(null)
const loadError = ref('')
const flash = ref('')
const hostFlash = ref('')

// 认证备份
const backupPwd = ref('')
const backupFlash = ref('')

async function doExport() {
  try {
    const r = JSON.parse(await CoreService.AuthBackupExport(backupPwd.value))
    backupFlash.value = r.canceled ? '已取消'
      : r.ok ? `已导出 ${r.bytes} 字节 → ${r.path}${r.encrypted ? '（加密）' : '（明文 JSON）'}`
      : `失败：${r.error}`
  } catch (e) { backupFlash.value = `失败：${e}` }
}

async function doImport() {
  try {
    const r = JSON.parse(await CoreService.AuthBackupImport(backupPwd.value))
    backupFlash.value = r.canceled ? '已取消'
      : r.ok ? `导入成功，恢复 ${r.imported} 个平台凭证`
      : `失败：${r.error}`
  } catch (e) { backupFlash.value = `失败：${e}` }
  setTimeout(() => (backupFlash.value = ''), 4000)
}

// 更新检查
const updateBusy = ref(false)
const updateFlash = ref('')
const updateInfo = ref<{ current: string; latest: string; hasUpdate: boolean; url: string } | null>(null)

async function doUpdateCheck() {
  updateBusy.value = true
  updateFlash.value = ''
  try {
    const r = await updateCheck()
    if (r.ok) {
      updateInfo.value = r
      updateFlash.value = r.hasUpdate ? '发现新版本' : '已是最新 ✓'
    } else {
      updateFlash.value = r.error ?? '检查失败'
    }
  } catch (e) {
    updateFlash.value = String(e)
  } finally {
    updateBusy.value = false
  }
}

// 代理 / ffmpeg（经 Go 宿主）
const sysHost = ref('')
const sysPort = ref(0)
const ffmpegPath = ref('')

function done(msg: string) {
  flash.value = msg
  setTimeout(() => (flash.value = ''), 1500)
}

async function save(key: string, value: string | number | boolean) {
  const r = await settingsSet(key, value)
  done(r.ok ? '已保存 ✓' : `失败：${r.error}`)
}

// ---- 按平台线程数：key 固定 downloadThreadsFor，platform 字段区分平台 ----
// 平台清单与网盘页 PLATFORMS 同源（10 平台）；settings_list 不回读该维度，输入留空 = 跟随全局线程数

const THREAD_PLATFORMS = [
  { key: 'quark', name: '夸克网盘' }, { key: 'uc', name: 'UC网盘' },
  { key: 'xunlei', name: '迅雷云盘' }, { key: 'baidu', name: '百度网盘' },
  { key: 'c139', name: '移动云盘' }, { key: 'pan123', name: '123云盘' },
  { key: 'pan115', name: '115网盘' }, { key: 'guangya', name: '光鸭云盘' },
  { key: 'ilanzou', name: '蓝奏云优享版' }, { key: 'lanzou', name: '蓝奏云' },
]
const perThreadOpen = ref(false)
const perThreads = ref<Record<string, number>>({})

async function savePlatformThreads(platform: string) {
  const v = Math.max(0, Math.floor(perThreads.value[platform] ?? 0))
  // 0 = 清除该平台的单独设置，恢复跟随全局（后端以 0 表示未单独设置）
  const r = await settingsSetPlatform('downloadThreadsFor', platform, v)
  done(r.ok ? '已保存 ✓' : `失败：${r.error}`)
}

async function onTheme(modeIdx: 0 | 1 | 2) {
  const mode = (['system', 'light', 'dark'] as const)[modeIdx]
  setThemeMode(mode)
  await save('darkMode', modeIdx)
}

// ---- 主题取色器（自定义种子色：themeColorMode=1 + themeSeedColor 十进制）----

const PRESET_SEEDS = ['#415F91', '#1565C0', '#00897B', '#2E7D32', '#6750A4', '#AD1457', '#BF360C', '#455A64']
const pickedSeed = ref<string>(cachedSeedColor() ?? '#415F91')

async function pickSeed(hex: string) {
  pickedSeed.value = hex
  applySeedColor(hex) // 立即生效；深浅色共用同一份覆盖（简化处理）
  if (!s.value) return
  s.value.themeColorMode = 1
  s.value.themeSeedColor = parseInt(hex.slice(1), 16)
  await save('themeColorMode', 1)
  await save('themeSeedColor', s.value.themeSeedColor)
}

function onColorInput(e: Event) {
  const hex = (e.target as HTMLInputElement).value
  if (/^#[0-9a-fA-F]{6}$/.test(hex)) pickSeed(hex)
}

// ---- GitHub 下载加速（自定义镜像前缀 + 触发测速）----

const mirrorPrefix = ref('')

async function saveMirror() {
  await save('githubMirrorPrefix', mirrorPrefix.value.trim())
}

async function doMirrorRefresh() {
  try {
    await githubMirrorRefresh()
    done('镜像测速已触发，稍后自动选最快节点 ✓')
  } catch (e) {
    done(`测速失败：${e}`)
  }
}

// ---- 忽略版本（kv：misc.ignored_version，更新检查核心侧据此跳过该版本）----

async function ignoreVersion() {
  if (!updateInfo.value?.latest) return
  const r = await kvPut('misc.ignored_version', updateInfo.value.latest)
  done(r ? `已忽略 ${updateInfo.value.latest}，之后检查更新不再提示 ✓` : '写入失败')
}

async function unignoreVersion() {
  const r = await kvPut('misc.ignored_version', '')
  done(r ? '已取消忽略 ✓' : '写入失败')
}

async function load() {
  try {
    s.value = await settingsList()
    // 自定义主题色回显：设置优先，读不到时沿用 localStorage 缓存
    if (s.value && s.value.themeColorMode === 1 && s.value.themeSeedColor > 0) {
      pickedSeed.value = seedNumToHex(s.value.themeSeedColor)
    }
    mirrorPrefix.value = s.value?.githubMirrorPrefix ?? ''
  } catch (e) {
    loadError.value = String(e)
  }
  try {
    const info = JSON.parse(await CoreService.HostStatus())
    sysHost.value = info.systemProxyHost ?? ''
    sysPort.value = info.systemProxyPort ?? 0
    ffmpegPath.value = info.ffmpegPath ?? ''
  } catch { /* 宿主状态不可用时保持默认 */ }
}

async function saveProxy() {
  if (!s.value) return
  try {
    const r = JSON.parse(await CoreService.SetProxy(s.value.proxyMode, s.value.proxyHost, s.value.proxyPort))
    hostFlash.value = r.ok ? '代理已注入核心并写入设置 ✓' : `失败：${r.error ?? '未知'}`
  } catch (e) { hostFlash.value = `失败：${e}` }
  setTimeout(() => (hostFlash.value = ''), 2500)
}

async function saveFfmpeg(auto = false) {
  try {
    const path = auto ? '' : ffmpegPath.value.trim()
    const r = JSON.parse(await CoreService.SetFfmpeg(path))
    hostFlash.value = r.ok
      ? (r.applied ? `ffmpeg 已注入：${r.applied}` : r.cleared ? '已清除注入（核心内置探测）' : '已保存 ✓')
      : `失败：${r.error ?? '未知'}`
    if (auto) ffmpegPath.value = ''
  } catch (e) { hostFlash.value = `失败：${e}` }
  setTimeout(() => (hostFlash.value = ''), 2500)
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div v-if="loadError" class="banner error">{{ loadError }}</div>

    <div v-if="s" class="card">
      <h3>外观（与原版互通）<span class="chip plain" style="margin-left: 8px">{{ flash }}</span></h3>
      <p class="hint">主题三态写 yunx/settings 的 dark_mode，Compose 原版改这里我们同步生效，反之亦然</p>
      <div class="row">
        <div class="seg">
          <button :class="{ active: s.darkMode === 0 }" @click="onTheme(0)">跟随系统</button>
          <button :class="{ active: s.darkMode === 1 }" @click="onTheme(1)">浅色</button>
          <button :class="{ active: s.darkMode === 2 }" @click="onTheme(2)">深色</button>
        </div>
      </div>
      <div class="row" style="margin-top: 14px">
        <span style="flex: 1">减少动画</span>
        <i class="switch" :class="{ on: s.reduceMotion }"
           @click="s.reduceMotion = !s.reduceMotion; save('reduceMotion', s.reduceMotion)" />
      </div>
      <div class="row" style="margin-top: 14px; gap: 10px">
        <span style="font-size: 13px; color: var(--on-bg-variant)">主题色</span>
        <button v-for="c in PRESET_SEEDS" :key="c" class="swatch" :class="{ picked: pickedSeed === c }"
                :style="{ background: c }" :title="c" @click="pickSeed(c)" />
        <input type="color" class="colorpick" :value="pickedSeed" title="自定义主题色"
               @input="onColorInput" />
      </div>
      <p class="hint" style="margin: 8px 0 0">选择即生效并写入 theme_seed_color（自定义主题色，深浅色共用）</p>
    </div>

    <div v-if="s" class="card">
      <h3>下载（正式设置）</h3>
      <div class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 1; min-width: 150px">
          <label>单任务线程数（1-512）</label>
          <input class="input" type="number" min="1" max="512" v-model.number="s.downloadThreads"
                 @change="save('downloadThreads', s.downloadThreads)" />
        </div>
        <div class="field" style="flex: 1; min-width: 150px">
          <label>最大并发任务（1-10）</label>
          <input class="input" type="number" min="1" max="10" v-model.number="s.maxConcurrentDownloads"
                 @change="save('maxConcurrentDownloads', s.maxConcurrentDownloads)" />
        </div>
        <div class="field" style="flex: 1; min-width: 150px">
          <label>失败重试（0-10）</label>
          <input class="input" type="number" min="0" max="10" v-model.number="s.downloadRetryCount"
                 @change="save('downloadRetryCount', s.downloadRetryCount)" />
        </div>
      </div>
      <div class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 1">
          <label>默认下载目录（空 = 系统下载目录）</label>
          <input class="input" v-model="s.downloadDirUri" placeholder="/home/xxx/Downloads"
                 @change="save('downloadDirUri', s.downloadDirUri ?? '')" />
        </div>
      </div>
      <div class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 1; min-width: 150px">
          <label>全局限速（字节/秒，0 = 不限）</label>
          <input class="input" type="number" min="0" v-model.number="s.downloadSpeedLimit"
                 @change="save('downloadSpeedLimit', s.downloadSpeedLimit)" />
        </div>
        <span style="flex: 1"></span>
      </div>
      <div class="row">
        <span style="flex: 1">下载期间阻止系统休眠</span>
        <i class="switch" :class="{ on: s.keepAwakeWhileDownloading }"
           @click="s.keepAwakeWhileDownloading = !s.keepAwakeWhileDownloading; save('keepAwakeWhileDownloading', s.keepAwakeWhileDownloading)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">剪贴板分享链接检测</span>
        <i class="switch" :class="{ on: s.clipboardLinkDetection }"
           @click="s.clipboardLinkDetection = !s.clipboardLinkDetection; save('clipboardLinkDetection', s.clipboardLinkDetection)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">免转存下载（夸克分享直下，不经网盘中转）</span>
        <i class="switch" :class="{ on: s.quarkNoSaveDownload }"
           @click="s.quarkNoSaveDownload = !s.quarkNoSaveDownload; save('quarkNoSaveDownload', s.quarkNoSaveDownload)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">通知栏显示下载进度（速度 / 百分比）</span>
        <i class="switch" :class="{ on: s.notificationShowSpeed }"
           @click="s.notificationShowSpeed = !s.notificationShowSpeed; save('notificationShowSpeed', s.notificationShowSpeed)" />
      </div>
      <!-- 按平台线程数：折叠面板，10 平台各一档（key 固定 downloadThreadsFor，platform 区分） -->
      <div class="row" style="margin-top: 14px; justify-content: space-between; cursor: pointer"
           @click="perThreadOpen = !perThreadOpen">
        <span style="font-weight: 600">按平台设置线程数</span>
        <span class="chip plain">{{ perThreadOpen ? '收起' : '展开' }}</span>
      </div>
      <div v-if="perThreadOpen" class="perthreads">
        <div v-for="p in THREAD_PLATFORMS" :key="p.key" class="field">
          <label>{{ p.name }}</label>
          <input class="input" type="number" min="0" max="512" v-model.number="perThreads[p.key]"
                 placeholder="0 = 跟随全局" @change="savePlatformThreads(p.key)" />
        </div>
      </div>
    </div>

    <div v-if="s" class="card">
      <h3>网络</h3>
      <div class="row" style="margin-bottom: 12px">
        <span style="flex: 1">HTTP/2</span>
        <i class="switch" :class="{ on: s.http2Enabled }"
           @click="s.http2Enabled = !s.http2Enabled; save('http2Enabled', s.http2Enabled)" />
      </div>
      <p class="hint">代理：探测在 Go 宿主，注入核心即时生效；三模式与原版一致，选择写入正式设置</p>
      <div class="row" style="margin-bottom: 12px">
        <div class="seg">
          <button :class="{ active: s.proxyMode === 'direct' }" @click="s.proxyMode = 'direct'">直连</button>
          <button :class="{ active: s.proxyMode === 'system' }" @click="s.proxyMode = 'system'">跟随系统</button>
          <button :class="{ active: s.proxyMode === 'manual' }" @click="s.proxyMode = 'manual'">手动</button>
        </div>
        <span v-if="sysHost" class="chip plain">系统探测：{{ sysHost }}:{{ sysPort }}</span>
        <span class="chip plain">{{ hostFlash }}</span>
      </div>
      <div v-if="s.proxyMode === 'manual'" class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 2; min-width: 160px">
          <label>代理服务器</label>
          <input class="input" v-model="s.proxyHost" placeholder="127.0.0.1" />
        </div>
        <div class="field" style="flex: 1; min-width: 90px">
          <label>端口</label>
          <input class="input" type="number" v-model.number="s.proxyPort" placeholder="7890" />
        </div>
      </div>
      <div class="row end"><button class="btn tonal small" @click="saveProxy">应用代理</button></div>
    </div>

    <div class="card">
      <h3>外部 ffmpeg（DASH 合轨）</h3>
      <p class="hint">路径由 Go 宿主注入核心；留空 = 自动探测 PATH</p>
      <div class="row" style="margin-bottom: 10px">
        <div class="field" style="flex: 1">
          <label>ffmpeg 可执行文件路径</label>
          <input class="input" v-model="ffmpegPath" placeholder="/usr/bin/ffmpeg" />
        </div>
      </div>
      <div class="row" style="justify-content: space-between">
        <span class="chip plain" v-if="sysHost && false"></span>
        <span class="chip plain">{{ hostFlash }}</span>
      </div>
      <div class="row end" style="margin-top: 10px">
        <button class="btn text small" @click="saveFfmpeg(true)">自动探测并注入</button>
        <button class="btn tonal small" @click="saveFfmpeg(false)">应用并保存</button>
      </div>
    </div>

    <div class="card">
      <h3>认证备份（与原版同格式，可跨设备恢复）</h3>
      <p class="hint">备份 10 网盘 + 视频解析平台 + GitHub Token 的凭证；口令 ≥8 位导出加密 .yunx，留空导出明文 .json</p>
      <div class="row" style="margin-bottom: 12px">
        <div class="field" style="flex: 1; min-width: 200px">
          <label>备份口令（导入 .yunx 时也填这里）</label>
          <input class="input" type="password" v-model="backupPwd" placeholder="至少 8 位，留空 = 明文 JSON" />
        </div>
      </div>
      <div class="row" style="justify-content: space-between">
        <span class="chip plain">{{ backupFlash }}</span>
        <span class="row">
          <button class="btn text small" @click="doImport">导入备份…</button>
          <button class="btn tonal small" @click="doExport">导出备份…</button>
        </span>
      </div>
    </div>

    <div class="card">
      <h3>更新</h3>
      <p class="hint">从 GitHub Release 检查最新版本（经镜像加速）</p>
      <div class="row" style="justify-content: space-between">
        <span class="chip plain">{{ updateFlash }}</span>
        <button class="btn tonal small" :disabled="updateBusy" @click="doUpdateCheck">
          {{ updateBusy ? '检查中…' : '检查更新' }}
        </button>
      </div>
      <div v-if="updateInfo" class="banner" style="margin-top: 10px">
        当前 {{ updateInfo.current }} · 最新 {{ updateInfo.latest }}
        <template v-if="updateInfo.hasUpdate">
          —— <a :href="updateInfo.url" target="_blank" style="color: var(--primary)">去下载</a>
        </template>
        <template v-else>（已是最新）</template>
      </div>
      <div v-if="s" class="row" style="margin-top: 12px">
        <span style="flex: 1">接受预发布版（beta / preview 也提示更新）</span>
        <i class="switch" :class="{ on: s.acceptPrereleaseUpdate }"
           @click="s.acceptPrereleaseUpdate = !s.acceptPrereleaseUpdate; save('acceptPrereleaseUpdate', s.acceptPrereleaseUpdate)" />
      </div>
      <div v-if="updateInfo" class="row end" style="margin-top: 10px">
        <button class="btn text small" @click="unignoreVersion">取消忽略</button>
        <button class="btn text small" @click="ignoreVersion">忽略此版本（{{ updateInfo.latest }}）</button>
      </div>
    </div>

    <div class="card">
      <h3>GitHub 下载加速</h3>
      <p class="hint">Release 附件直链可经自定义前缀加速（如镜像站反代前缀，留空 = 直连 GitHub）；「测速」触发核心对各公共镜像测速</p>
      <div class="row" style="margin-bottom: 10px">
        <div class="field" style="flex: 1; min-width: 220px">
          <label>下载前缀（拼接在 github.com 链接前，留空 = 直连）</label>
          <input v-model="mirrorPrefix" class="input" placeholder="https://gh-proxy.com/"
                 @keydown.enter="saveMirror" @change="saveMirror" />
        </div>
        <button class="btn tonal small" style="align-self: flex-end" @click="doMirrorRefresh">测速</button>
      </div>
      <p class="hint" style="margin: 0">更新检查与附件下载共用此设置，保存后立即生效</p>
    </div>

    <div v-if="s" class="card">
      <h3>实验性功能</h3>
      <p class="hint">以下能力仍在验证阶段，默认关闭；开启后行为可能随核心更新调整</p>
      <div class="row">
        <span style="flex: 1">多账号轮换下载（同平台多账号轮流取直链）</span>
        <i class="switch" :class="{ on: s.multiAccountDownload }"
           @click="s.multiAccountDownload = !s.multiAccountDownload; save('multiAccountDownload', s.multiAccountDownload)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">多源下载（同一文件聚合多镜像源加速）</span>
        <i class="switch" :class="{ on: s.multiSourceDownload }"
           @click="s.multiSourceDownload = !s.multiSourceDownload; save('multiSourceDownload', s.multiSourceDownload)" />
      </div>
      <div class="row" style="margin-top: 10px">
        <span style="flex: 1">下载完整文件校验（SHA-256 全量）</span>
        <i class="switch" :class="{ on: s.fullFileSha256 }"
           @click="s.fullFileSha256 = !s.fullFileSha256; save('fullFileSha256', s.fullFileSha256)" />
      </div>
    </div>

    <div v-if="s" class="card">
      <h3>BT / 磁力</h3>
      <p class="hint">磁力与种子下载的 peer 发现配置；保存后对新任务生效</p>
      <div class="row" style="margin-bottom: 12px">
        <span style="flex: 1">DHT 节点发现（无 tracker 时也能找 peer）</span>
        <i class="switch" :class="{ on: s.btDhtEnabled }"
           @click="s.btDhtEnabled = !s.btDhtEnabled; save('btDhtEnabled', s.btDhtEnabled)" />
      </div>
      <div class="field">
        <label>Tracker 列表（每行一个 tracker URL）</label>
        <textarea v-model="s.btTrackers" class="input trackers" rows="6"
                  placeholder="https://tracker.example.com/announce" @change="save('btTrackers', s.btTrackers)" />
      </div>
      <p class="hint" style="margin: 8px 0 0">每行一条，空行与 # 开头的注释行会被忽略</p>
    </div>

    <div class="card">
      <h3>更多分组<span class="tag-m6">后续批次</span></h3>
      <p class="hint">储存目录迁移 / 数据库加密档位 / Gopeed 引擎管理 —— 待后续批次接入</p>
    </div>

    <div class="row end">
      <button class="btn text small" @click="router.push('/about')">关于 YunX</button>
    </div>
  </div>
</template>

<style scoped>
.swatch { width: 26px; height: 26px; border-radius: 50%; border: 2px solid var(--outline-variant);
  cursor: pointer; padding: 0; flex: none; }
.swatch.picked { border-color: var(--on-bg); box-shadow: 0 0 0 2px var(--surface), 0 0 0 4px var(--on-bg); }
.colorpick { width: 30px; height: 30px; padding: 2px; border: none; background: none; cursor: pointer; flex: none; }
.perthreads { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 10px; margin-top: 10px; padding-top: 12px; border-top: 1px dashed var(--outline-variant); }
.trackers { font-family: ui-monospace, 'Cascadia Mono', Menlo, Consolas, monospace; font-size: 12.5px;
  resize: vertical; line-height: 1.6; }
</style>
