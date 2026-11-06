<script setup lang="ts">
// 公告面板：铃铛入口的覆盖层（列表 + 详情 + 已读标记，与原版同源 read_ids）。
import { computed, onMounted, ref } from 'vue'
import { announcementDetail, announcementsList, type AnnouncementItem } from '../core'

const emit = defineEmits<{ (e: 'close'): void }>()

const items = ref<AnnouncementItem[]>([])
const error = ref('')
const loading = ref(true)
const detail = ref<{ title: string; content: string } | null>(null)
const detailLoading = ref(false)

const unread = computed(() => items.value.filter((a) => !a.read).length)

async function load() {
  loading.value = true
  try {
    const r = await announcementsList()
    if (r.ok) items.value = r.announcements ?? []
    else error.value = r.error ?? '获取失败'
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
  }
}

async function open(item: AnnouncementItem) {
  detailLoading.value = true
  detail.value = null
  try {
    const r = await announcementDetail(item.id)
    if (r.ok) {
      detail.value = { title: r.title, content: r.content ?? '（无正文）' }
      item.read = true // 详情即已读（核心已 markRead）
    } else {
      error.value = r.error ?? '详情获取失败'
    }
  } finally {
    detailLoading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="mask" @click.self="emit('close')">
    <div class="dlg">
      <div class="row" style="justify-content: space-between; margin-bottom: 8px">
        <h3 style="margin: 0">
          公告 <span v-if="unread" class="chip">{{ unread }} 未读</span>
        </h3>
        <button class="btn text small" @click="emit('close')">关闭</button>
      </div>

      <template v-if="detail">
        <button class="btn text small" @click="detail = null">← 返回列表</button>
        <div v-if="detailLoading" class="banner" style="margin-top: 8px">加载正文…</div>
        <template v-else>
          <h4 style="margin: 12px 0 6px">{{ detail.title }}</h4>
          <div class="content" v-html="detail.content" />
        </template>
      </template>

      <template v-else>
        <div v-if="error" class="banner error">{{ error }}</div>
        <div v-else-if="loading" class="banner">加载中…</div>
        <div v-else-if="items.length === 0" class="empty" style="padding: 18px 0">暂无公告</div>
        <div class="list">
          <div v-for="a in items" :key="a.id" class="item" @click="open(a)">
            <div class="row" style="justify-content: space-between; gap: 8px">
              <b class="title">
                <span v-if="a.isPinned" class="chip" style="margin-right: 6px">置顶</span>{{ a.title }}
              </b>
              <span class="date">{{ a.publishAt?.slice(0, 10) ?? '' }}</span>
            </div>
            <div class="summary">{{ a.summary }}</div>
            <i v-if="!a.read" class="dot" />
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.mask { position: fixed; inset: 0; background: rgba(0,0,0,0.42); display: flex;
  align-items: center; justify-content: center; z-index: 50; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(640px, 92vw); max-height: 76vh; display: flex; flex-direction: column;
  border: 1px solid var(--outline-variant); overflow: hidden; }
.list { overflow: auto; }
.item { position: relative; padding: 10px 6px; border-bottom: 1px solid var(--outline-variant); cursor: pointer; }
.item:hover { background: var(--surface-container); }
.title { font-size: 14px; }
.date { font-size: 12px; color: var(--on-bg-variant); flex: none; }
.summary { font-size: 12.5px; color: var(--on-bg-variant); margin-top: 4px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dot { position: absolute; top: 12px; right: 6px; width: 8px; height: 8px;
  border-radius: 50%; background: var(--error); }
.content { overflow: auto; font-size: 13.5px; line-height: 1.7; }
/* 公告正文可能含富文本/HTML（Markdown 渲染结果）：基础排版样式兜底 */
.content :deep(img) { max-width: 100%; border-radius: 8px; }
.content :deep(a) { color: var(--primary); }
.content :deep(p) { margin: 0.6em 0; }
.content :deep(code) { background: var(--surface-container); border-radius: 4px;
  padding: 1px 5px; font-size: 12.5px; font-family: ui-monospace, 'Cascadia Mono', Menlo, Consolas, monospace; }
.content :deep(pre) { background: var(--surface-container); border-radius: 8px;
  padding: 10px 12px; overflow: auto; }
.content :deep(pre code) { background: transparent; padding: 0; }
.content :deep(h1), .content :deep(h2), .content :deep(h3), .content :deep(h4) {
  margin: 0.9em 0 0.4em; font-size: 1.05em; }
.content :deep(ul), .content :deep(ol) { padding-left: 1.4em; margin: 0.6em 0; }
.content :deep(blockquote) { margin: 0.6em 0; padding: 4px 12px; border-left: 3px solid var(--outline);
  color: var(--on-bg-variant); }
</style>
