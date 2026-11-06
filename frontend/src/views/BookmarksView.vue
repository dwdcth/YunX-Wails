<script setup lang="ts">
// 书签页：收藏的分享链接列表（与原版同一 bookmark 表互通）。
// 重解析：链接放进 localStorage['yunx.pendingResolve']，由解析页 onMounted 检取自动 parse。
// 新增/改分类：对齐原版 AddBookmarkDialog / EditCategoryDialog（核心无 update 命令，
// 改分类 = 读原条目全部字段 → bookmark_delete + bookmark_add 重建，createTime 会刷新）。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { bookmarkAdd, bookmarkDelete, bookmarkList, parseShare, type BookmarkItem } from '../core'

const router = useRouter()
const items = ref<BookmarkItem[]>([])
const error = ref('')
const loading = ref(true)
const category = ref('') // 空 = 全部

// 分类从全量书签前端聚合（核心没有分类维度接口）
const categories = computed(() => [...new Set(items.value.map((b) => b.category).filter(Boolean))])
const filtered = computed(() =>
  category.value ? items.value.filter((b) => b.category === category.value) : items.value)

async function refresh() {
  loading.value = true
  try {
    items.value = await bookmarkList()
    error.value = ''
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
  }
}

function reresolve(b: BookmarkItem) {
  localStorage.setItem('yunx.pendingResolve', b.link)
  router.push('/resolve')
}

async function remove(b: BookmarkItem) {
  if (!confirm(`删除书签「${b.title || b.link}」？`)) return
  try {
    await bookmarkDelete(b.id)
    await refresh()
  } catch (e) {
    error.value = String(e)
  }
}

// ---- 新增书签弹窗（标题/链接/分类可空/提取码可空，平台本地识别） ----

const GITHUB_RE = /^https?:\/\/github\.com\//i

const addOpen = ref(false)
const addBusy = ref(false)
const addError = ref('')
const addLink = ref('')
const addTitle = ref('')
const addPwd = ref('')
const addCatSel = ref('未分类')
const addCatCustom = ref('')

/** 弹窗里可选的分类：已有分类 + 「未分类」默认项（去重） */
const addCategoryChoices = computed(() => [...new Set(['未分类', ...categories.value])])

function openAdd() {
  addLink.value = ''
  addTitle.value = ''
  addPwd.value = ''
  addCatSel.value = '未分类'
  addCatCustom.value = ''
  addError.value = ''
  addOpen.value = true
}

/** 平台识别走核心纯本地解析（对齐原版 ShareLinkParser.parse + GitHub 兜底），失败不阻塞 */
async function detectPlatform(link: string): Promise<{ platform: string; pwd: string }> {
  try {
    const r = await parseShare(link)
    if (r.found && r.platform) return { platform: r.platform, pwd: r.pwd ?? '' }
  } catch { /* 识别失败按未知平台收藏 */ }
  return { platform: GITHUB_RE.test(link) ? 'GITHUB' : '', pwd: '' }
}

async function submitAdd() {
  const link = addLink.value.trim()
  if (!link || addBusy.value) return
  addBusy.value = true
  addError.value = ''
  try {
    const detected = await detectPlatform(link)
    const pwd = addPwd.value.trim() || detected.pwd
    const cat = addCatCustom.value.trim() || addCatSel.value
    const r = await bookmarkAdd(link, addTitle.value.trim(), detected.platform, pwd, cat)
    if (!r.ok) {
      addError.value = r.error ?? '保存失败'
      return
    }
    addOpen.value = false
    await refresh()
  } catch (e) {
    addError.value = String(e)
  } finally {
    addBusy.value = false
  }
}

// ---- 修改分类弹窗（核心无 bookmark_update：删旧重建，其余字段原样保留） ----

const editing = ref<BookmarkItem | null>(null)
const editBusy = ref(false)
const editError = ref('')
const editCatSel = ref('')
const editCatCustom = ref('')

const editCategoryChoices = computed(() => {
  const cur = editing.value?.category || '未分类'
  return [...new Set([cur, '未分类', ...categories.value])]
})

function openEditCategory(b: BookmarkItem) {
  editing.value = b
  editCatSel.value = b.category || '未分类'
  editCatCustom.value = ''
  editError.value = ''
}

async function submitEditCategory() {
  const b = editing.value
  if (!b || editBusy.value) return
  const cat = editCatCustom.value.trim() || editCatSel.value.trim()
  if (!cat) return
  editBusy.value = true
  editError.value = ''
  try {
    // 先重建后删旧：中途失败时旧条目仍在，不至于丢数据
    const r = await bookmarkAdd(b.link, b.title, b.platform, b.pwd, cat)
    if (!r.ok) {
      editError.value = r.error ?? '保存失败'
      return
    }
    await bookmarkDelete(b.id)
    editing.value = null
    await refresh()
  } catch (e) {
    editError.value = String(e)
  } finally {
    editBusy.value = false
  }
}

onMounted(refresh)
</script>

<template>
  <div class="page">
    <div class="card">
      <div class="row" style="justify-content: space-between; margin-bottom: 4px">
        <h3 style="margin: 0">书签收藏</h3>
        <button class="btn tonal small" @click="openAdd">+ 新增书签</button>
      </div>
      <p class="hint">解析页收藏的分享链接，也可手动新增；「重解析」会跳到解析页并自动解析</p>
      <div v-if="categories.length" class="row" style="gap: 8px">
        <button class="chip fchip" :class="{ plain: category !== '' }" @click="category = ''">全部</button>
        <button v-for="c in categories" :key="c" class="chip fchip"
                :class="{ plain: category !== c }" @click="category = c">{{ c }}</button>
      </div>
    </div>

    <div v-if="error" class="banner error">{{ error }}</div>
    <div v-else-if="loading" class="card"><div class="banner">加载中…</div></div>
    <div v-else-if="filtered.length === 0" class="card">
      <div class="empty">
        <div class="big">⭐</div>
        <h3 style="margin: 0 0 6px">还没有书签</h3>
        <p>在解析页解析成功后点「收藏」保存到这里，或点下方按钮手动新增。</p>
        <button class="btn tonal" style="margin-top: 10px" @click="openAdd">新增书签</button>
      </div>
    </div>

    <div v-for="b in filtered" :key="b.id" class="card" style="padding: 14px 20px">
      <div class="row" style="justify-content: space-between">
        <div style="min-width: 0; flex: 1">
          <div style="font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap"
               :title="b.title || b.link">
            {{ b.title || b.link }}
          </div>
          <a class="blink" :href="b.link" target="_blank" :title="b.link">{{ b.link }}</a>
        </div>
        <span class="row" style="flex: none">
          <span class="chip">{{ b.platform }}</span>
          <span v-if="b.category" class="chip plain">{{ b.category }}</span>
          <span v-if="b.pwd" class="chip plain">pwd {{ b.pwd }}</span>
          <button class="btn tonal small" @click="reresolve(b)">重解析</button>
          <button class="btn text small" @click="openEditCategory(b)">改分类</button>
          <button class="btn text small" style="color: var(--error)" @click="remove(b)">删除</button>
        </span>
      </div>
    </div>

    <!-- 新增书签弹窗（对齐原版 AddBookmarkDialog：链接必填，标题/提取码/分类可空） -->
    <div v-if="addOpen" class="mask" @click.self="addOpen = false">
      <div class="dlg">
        <div class="row" style="justify-content: space-between; margin-bottom: 10px">
          <h3 style="margin: 0">新增书签</h3>
          <button class="btn text small" @click="addOpen = false">关闭</button>
        </div>
        <div style="display: flex; flex-direction: column; gap: 10px">
          <div class="field">
            <label>分享链接（必填，平台自动识别）</label>
            <input v-model="addLink" class="input" placeholder="https://pan.quark.cn/s/xxxxxxxx"
                   @keydown.enter="submitAdd" />
          </div>
          <div class="field">
            <label>标题（可空，不填显示链接）</label>
            <input v-model="addTitle" class="input" placeholder="如：某某资源合集"
                   @keydown.enter="submitAdd" />
          </div>
          <div class="field">
            <label>提取码（可空，文案里自带会自动带出）</label>
            <input v-model="addPwd" class="input" placeholder="如 a1b2" @keydown.enter="submitAdd" />
          </div>
          <div class="field">
            <label>分类（选已有分类或自定义，可空 = 未分类）</label>
            <div class="row" style="gap: 8px">
              <button v-for="c in addCategoryChoices" :key="c" class="chip fchip"
                      :class="{ plain: addCatSel !== c || !!addCatCustom.trim() }"
                      @click="addCatSel = c; addCatCustom = ''">{{ c }}</button>
            </div>
            <input v-model="addCatCustom" class="input" placeholder="自定义分类（填了则优先生效）"
                   @keydown.enter="submitAdd" />
          </div>
        </div>
        <div v-if="addError" class="banner error" style="margin-top: 10px">{{ addError }}</div>
        <div class="row end" style="margin-top: 12px">
          <button class="btn" :disabled="!addLink.trim() || addBusy" @click="submitAdd">
            {{ addBusy ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 修改分类弹窗（对齐原版 EditCategoryDialog；核心无 update，删旧重建） -->
    <div v-if="editing" class="mask" @click.self="editing = null">
      <div class="dlg">
        <div class="row" style="justify-content: space-between; margin-bottom: 10px">
          <h3 style="margin: 0">修改分类</h3>
          <button class="btn text small" @click="editing = null">关闭</button>
        </div>
        <p class="h2line" :title="editing.title || editing.link">{{ editing.title || editing.link }}</p>
        <div class="row" style="gap: 8px; margin-bottom: 10px">
          <button v-for="c in editCategoryChoices" :key="c" class="chip fchip"
                  :class="{ plain: editCatSel !== c || !!editCatCustom.trim() }"
                  @click="editCatSel = c; editCatCustom = ''">{{ c }}</button>
        </div>
        <div class="field">
          <label>自定义分类（填了则优先生效）</label>
          <input v-model="editCatCustom" class="input" placeholder="留空则用上面选中的分类"
                 @keydown.enter="submitEditCategory" />
        </div>
        <div v-if="editError" class="banner error" style="margin-top: 10px">{{ editError }}</div>
        <div class="row end" style="margin-top: 12px">
          <button class="btn" :disabled="editBusy || !(editCatCustom.trim() || editCatSel.trim())"
                  @click="submitEditCategory">
            {{ editBusy ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.blink { display: block; font-size: 12.5px; color: var(--on-bg-variant); margin-top: 3px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fchip { border: none; cursor: pointer; font-family: inherit; }

.mask { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.42); display: flex;
  align-items: center; justify-content: center; z-index: 40; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(520px, 92vw); border: 1px solid var(--outline-variant); }
.h2line { margin: 0 0 10px; font-size: 13px; color: var(--on-bg-variant);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
