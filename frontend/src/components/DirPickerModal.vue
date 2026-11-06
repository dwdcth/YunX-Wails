<script setup lang="ts">
// 目录选择弹窗：浏览自己云盘的文件夹树（只列目录），用于「移动到」「转存到」。
// 百度网的 fidToken 是路径，确认时一并回传（move 需要 targetPath）。
import { ref, watch } from 'vue'
import { cloudList, type ShareFile } from '../core'
import AppIcon from './AppIcon.vue'

const props = defineProps<{ platform: string; title: string }>()
const emit = defineEmits<{ (e: 'confirm', fid: string, path: string): void; (e: 'cancel'): void }>()

const dirs = ref<ShareFile[]>([])
const stack = ref<{ fid: string; name: string; path: string }[]>([])
const loading = ref(false)
const error = ref('')

async function open(fid: string, name: string, path: string) {
  loading.value = true
  error.value = ''
  try {
    const r = await cloudList(props.platform, fid)
    if (r.ok && r.files) {
      dirs.value = r.files.filter((f) => f.isdir)
      if (fid !== stack.value[stack.value.length - 1]?.fid) {
        stack.value.push({ fid, name, path })
      }
    } else {
      error.value = r.error ?? '列目录失败'
    }
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
  }
}

watch(() => props.platform, async (p) => {
  if (!p) return
  stack.value = []
  const r = await cloudList(p).catch(() => null)
  if (r?.ok) {
    stack.value = [{ fid: r.dirFid, name: '根目录', path: '' }]
    dirs.value = (r.files ?? []).filter((f) => f.isdir)
  }
}, { immediate: true })

function currentDir() {
  return stack.value[stack.value.length - 1]
}

function confirmPick() {
  const d = currentDir()
  // 百度的目录 fidToken 即路径，其它平台为空
  const path = dirs.value.find((f) => f.fid === d.fid)?.fidToken ?? ''
  emit('confirm', d.fid, d.path || path)
}
</script>

<template>
  <div class="mask" @click.self="emit('cancel')">
    <div class="dlg">
      <div class="row" style="justify-content: space-between; margin-bottom: 8px">
        <h3 style="margin: 0">{{ title }}</h3>
        <button class="btn text small" @click="emit('cancel')">关闭</button>
      </div>
      <div class="row crumbs" style="margin-bottom: 8px">
        <template v-for="(d, i) in stack" :key="d.fid + i">
          <button class="crumb" :disabled="i === stack.length - 1 || loading"
                  @click="i < stack.length - 1 && open(d.fid, d.name, d.path)">{{ d.name }}</button>
          <span v-if="i < stack.length - 1" class="sep">/</span>
        </template>
      </div>
      <div v-if="error" class="banner error">{{ error }}</div>
      <div v-else-if="loading" class="banner">加载…</div>
      <div v-else-if="dirs.length === 0" class="empty" style="padding: 18px 0">（没有子文件夹）</div>
      <div class="list">
        <div v-for="d in dirs" :key="d.fid" class="row pick" @click="open(d.fid, d.fname, d.fidToken)">
          <AppIcon name="cloud" :size="18" /><span>{{ d.fname }}</span>
        </div>
      </div>
      <div class="row end" style="margin-top: 12px">
        <button class="btn tonal small" :disabled="loading" @click="confirmPick">
          选定「{{ currentDir()?.name ?? '根目录' }}」
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask { position: fixed; inset: 0; background: rgba(0,0,0,0.42); display: flex;
  align-items: center; justify-content: center; z-index: 40; }
.dlg { background: var(--surface); border-radius: var(--radius-lg); padding: 18px 20px;
  width: min(520px, 92vw); max-height: 78vh; display: flex; flex-direction: column;
  border: 1px solid var(--outline-variant); }
.list { overflow: auto; min-height: 120px; }
.pick { padding: 8px 6px; border-bottom: 1px solid var(--outline-variant); cursor: pointer; gap: 8px; }
.pick:hover { background: var(--surface-container); }
.crumbs { flex-wrap: wrap; }
.crumb { border: none; background: transparent; color: var(--primary); cursor: pointer;
  padding: 2px 6px; border-radius: 6px; font-size: 13px; }
.crumb:disabled { color: var(--on-bg); font-weight: 600; }
.sep { color: var(--outline); }
</style>
