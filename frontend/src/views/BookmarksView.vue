<script setup lang="ts">
// 书签页：收藏的分享链接列表（与原版同一 bookmark 表互通）。
// 重解析：链接放进 localStorage['yunx.pendingResolve']，由解析页 onMounted 检取自动 parse。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { bookmarkDelete, bookmarkList, type BookmarkItem } from '../core'

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

onMounted(refresh)
</script>

<template>
  <div class="page">
    <div class="card">
      <h3>书签收藏</h3>
      <p class="hint">解析页收藏的分享链接；「重解析」会跳到解析页并自动解析</p>
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
        <p>在解析页解析成功后点「收藏」即可保存到这里。</p>
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
          <button class="btn text small" style="color: var(--error)" @click="remove(b)">删除</button>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.blink { display: block; font-size: 12.5px; color: var(--on-bg-variant); margin-top: 3px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fchip { border: none; cursor: pointer; font-family: inherit; }
</style>
