<script lang="ts">
import { defineComponent } from 'vue';
import { api, Provider, TestResult } from '../api';
import { toast, confirmDialog } from '../toast';
import Modal from '../components/Modal.vue';
import Toggle from '../components/Toggle.vue';
import IconPlus from '~icons/lucide/plus';
import IconPencil from '~icons/lucide/pencil';
import IconTrash from '~icons/lucide/trash-2';
import IconRefresh from '~icons/lucide/refresh-cw';
import IconPlug from '~icons/lucide/plug-zap';
import IconChevron from '~icons/lucide/chevron-down';
import IconX from '~icons/lucide/x';
import IconCheck from '~icons/lucide/check';
import IconAlert from '~icons/lucide/circle-alert';

interface ProviderForm {
  id: string;
  name: string;
  anthropic_url: string;
  openai_url: string;
  api_key: string;
  priority: number;
  timeout_sec: number;
  enabled: boolean;
  note: string;
}

function emptyForm(): ProviderForm {
  return { id: '', name: '', anthropic_url: '', openai_url: '', api_key: '', priority: 10, timeout_sec: 600, enabled: true, note: '' };
}

export default defineComponent({
  name: 'ProvidersView',
  components: { Modal, Toggle, IconPlus, IconPencil, IconTrash, IconRefresh, IconPlug, IconChevron, IconX, IconCheck, IconAlert },
  data() {
    return {
      providers: [] as Provider[],
      loading: false,
      showEditor: false,
      form: emptyForm(),
      expanded: '' as string,
      fetching: '' as string,
      testing: '' as string,
      testingAll: false,
      newModel: '' as string,
    };
  },
  mounted() {
    this.load();
  },
  methods: {
    load(): void {
      this.loading = true;
      api.providers().then((p) => { this.providers = p || []; }).catch((e) => toast('error', e.message)).finally(() => { this.loading = false; });
    },
    openCreate(): void {
      this.form = emptyForm();
      this.showEditor = true;
    },
    openEdit(p: Provider): void {
      this.form = {
        id: p.id, name: p.name, anthropic_url: p.anthropic_url, openai_url: p.openai_url,
        api_key: '', priority: p.priority, timeout_sec: p.timeout_sec, enabled: p.enabled, note: p.note,
      };
      this.showEditor = true;
    },
    save(): void {
      const payload = { ...this.form };
      if (!payload.api_key) { (payload as Record<string, unknown>).api_key = undefined; }
      const req = payload.id ? api.updateProvider(payload.id, payload) : api.createProvider(payload);
      req.then(() => {
        this.showEditor = false;
        toast('success', payload.id ? `供应商「${payload.name}」已保存` : `供应商「${payload.name}」已添加`);
        this.load();
      }).catch((e) => toast('error', e.message));
    },
    async remove(p: Provider): Promise<void> {
      const okGo = await confirmDialog(`确定删除供应商「${p.name}」?\n其关联路由/通道引用将一并清除。`, '删除供应商', '删除');
      if (!okGo) return;
      try {
        await api.deleteProvider(p.id);
        toast('success', `供应商「${p.name}」已删除`);
        this.load();
      } catch (e) {
        toast('error', (e as Error).message);
      }
    },
    toggleExpand(id: string): void {
      this.expanded = this.expanded === id ? '' : id;
      this.newModel = '';
    },
    async fetchModels(p: Provider): Promise<void> {
      this.fetching = p.id;
      try {
        const r = await api.fetchModels(p.id);
        this.expanded = p.id;
        if (r.errors.length) {
          toast('info', `已同步 ${r.added} 个模型到模型池(共 ${r.models.length} 个)\n部分失败: ${r.errors.join('; ')}`, 5000);
        } else {
          toast('success', `已同步 ${r.added} 个模型到模型池(共 ${r.models.length} 个)`);
        }
        this.load();
      } catch (e) {
        toast('error', (e as Error).message);
      } finally {
        this.fetching = '';
      }
    },
    async test(p: Provider): Promise<void> {
      this.testing = p.id;
      try {
        const r = await api.testProvider(p.id);
        if (r.ok) toast('success', `「${p.name}」连通正常 · ${r.latency_ms}ms`);
        else toast('error', `「${p.name}」连通失败: ${r.errors.join('; ')}`, 5000);
        this.load();
      } catch (e) {
        toast('error', (e as Error).message);
      } finally {
        this.testing = '';
      }
    },
    async testAll(): Promise<void> {
      this.testingAll = true;
      try {
        const results = await api.testAllProviders();
        const okN = results.filter((r) => r.ok).length;
        const fail = results.filter((r) => !r.ok);
        if (fail.length === 0) toast('success', `全部 ${okN} 个供应商连通正常`);
        else toast('error', `${okN} 正常, ${fail.length} 异常: ${fail.map((f) => f.name).join('、')}`, 5000);
        this.load();
      } catch (e) {
        toast('error', (e as Error).message);
      } finally {
        this.testingAll = false;
      }
    },
    async addModel(p: Provider): Promise<void> {
      const m = this.newModel.trim();
      if (!m) return;
      const pool = [...(p.fetched_models || []), m];
      this.newModel = '';
      await api.setProviderModels(p.id, pool);
      this.load();
    },
    async removeModel(p: Provider, m: string): Promise<void> {
      const pool = (p.fetched_models || []).filter((x) => x !== m);
      await api.setProviderModels(p.id, pool);
      this.load();
    },
    testBadge(p: Provider): { cls: string; text: string } {
      const t: TestResult | null | undefined = p.last_test;
      if (!t) return { cls: '', text: '' };
      const ago = this.fmtAgo(t.tested_at);
      if (t.ok) return { cls: 'bg-emerald-50 text-emerald-600 dark:bg-emerald-950/50 dark:text-emerald-400', text: `正常 · ${t.latency_ms}ms · ${ago}` };
      const firstErr = (t.errors || [])[0] || '未知错误';
      return { cls: 'bg-rose-50 text-rose-600 dark:bg-rose-950/50 dark:text-rose-400', text: `异常 · ${ago}` + (firstErr.length > 60 ? '' : ' · ' + firstErr) };
    },
    fmtAgo(ts: number): string {
      const s = Math.floor(Date.now() / 1000) - ts;
      if (s < 60) return '刚刚';
      if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
      if (s < 86400) return `${Math.floor(s / 3600)} 小时前`;
      return `${Math.floor(s / 86400)} 天前`;
    },
  },
});
</script>

<template>
  <div>
    <div class="mb-5 flex items-center justify-between">
      <h1 class="text-xl font-bold">供应商</h1>
      <div class="flex gap-2">
        <button class="btn-ghost" :disabled="testingAll" @click="testAll">
          <IconPlug class="h-4 w-4" :class="testingAll && 'animate-pulse'" /> {{ testingAll ? '测试中…' : '测试全部' }}
        </button>
        <button class="btn-primary" @click="openCreate"><IconPlus class="h-4 w-4" /> 新增供应商</button>
      </div>
    </div>

    <div class="space-y-3">
      <div v-for="p in providers" :key="p.id" class="card p-4">
        <div class="flex flex-wrap items-center gap-3">
          <span class="badge" :class="p.enabled ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300' : 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400'">
            {{ p.enabled ? '启用' : '停用' }}
          </span>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2 text-sm font-semibold">
              {{ p.name }}
              <span class="text-xs font-normal text-slate-400">优先级 {{ p.priority }}</span>
            </div>
            <div class="mt-0.5 truncate text-xs text-slate-400">
              <span v-if="p.anthropic_url">anthropic: {{ p.anthropic_url }}</span>
              <span v-if="p.openai_url" class="ml-2">openai: {{ p.openai_url }}</span>
            </div>
          </div>
          <div class="flex items-center gap-1">
            <button class="btn-ghost" :disabled="testing === p.id" @click="test(p)"><IconPlug class="h-4 w-4" /> {{ testing === p.id ? '测试中…' : '测试' }}</button>
            <button class="btn-ghost" :disabled="fetching === p.id" @click="fetchModels(p)"><IconRefresh class="h-4 w-4" :class="fetching === p.id && 'animate-spin'" /> 拉取模型</button>
            <button class="btn-ghost" @click="openEdit(p)"><IconPencil class="h-4 w-4" /></button>
            <button class="btn-danger" @click="remove(p)"><IconTrash class="h-4 w-4" /></button>
            <button v-if="(p.fetched_models || []).length" class="btn-ghost !px-2" @click="toggleExpand(p.id)">
              <IconChevron class="h-4 w-4 transition-transform" :class="expanded === p.id && 'rotate-180'" />
            </button>
          </div>
        </div>
        <div v-if="p.last_test" class="mt-2 flex items-center gap-1.5 text-xs">
          <span class="badge" :class="testBadge(p).cls">
            <IconCheck v-if="p.last_test.ok" class="h-3.5 w-3.5" />
            <IconAlert v-else class="h-3.5 w-3.5" />
            {{ testBadge(p).text }}
          </span>
        </div>
        <div v-if="expanded === p.id" class="mt-3 rounded-xl bg-slate-50 p-3 dark:bg-slate-950/60">
          <div class="text-xs font-medium text-slate-400">模型池 · {{ (p.fetched_models || []).length }} 个(供路由/同名切换匹配, 可删改)</div>
          <div class="mt-2 flex flex-wrap items-center gap-1.5">
            <span v-for="m in p.fetched_models || []" :key="m" class="badge bg-white font-mono text-[11px] text-slate-500 ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
              {{ m }}
              <button class="ml-0.5 text-slate-300 transition hover:text-rose-500" title="从模型池移除" @click="removeModel(p, m)">
                <IconX class="h-3 w-3" />
              </button>
            </span>
            <span class="inline-flex items-center">
              <input
                v-model="newModel"
                class="input !w-40 !py-1 font-mono text-[11px]"
                placeholder="手动添加模型名"
                @keyup.enter="addModel(p)"
              />
              <button class="btn-ghost !px-1.5 !py-1" title="添加" @click="addModel(p)"><IconPlus class="h-3.5 w-3.5" /></button>
            </span>
          </div>
        </div>
      </div>
      <div v-if="!providers.length && !loading" class="card p-10 text-center text-sm text-slate-400">
        还没有供应商。新增一个, 例如 Anthropic 官方填 https://api.anthropic.com
      </div>
    </div>

    <Modal :open="showEditor" :title="form.id ? '编辑供应商' : '新增供应商'" @close="showEditor = false">
      <form class="space-y-3" @submit.prevent="save">
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label">名称 *</label>
            <input v-model="form.name" class="input" placeholder="例如 Kimi / DeepSeek 官方" required />
          </div>
          <div>
            <label class="label">API Key {{ form.id ? '(留空则不修改)' : '*' }}</label>
            <input v-model="form.api_key" class="input" type="password" :required="!form.id" :placeholder="form.id ? '已加密保存' : 'sk-...'" />
          </div>
        </div>
        <div>
          <label class="label">Anthropic 格式 BASE_URL(供 Claude Code 使用)</label>
          <input v-model="form.anthropic_url" class="input" placeholder="https://api.anthropic.com 或 https://api.kimi.com/coding/" />
        </div>
        <div>
          <label class="label">OpenAI 格式 BASE_URL(供 OpenAI 协议客户端使用)</label>
          <input v-model="form.openai_url" class="input" placeholder="https://api.xx.com/v1" />
        </div>
        <div class="grid grid-cols-3 gap-3">
          <div>
            <label class="label">优先级(小者优先)</label>
            <input v-model.number="form.priority" class="input" type="number" />
          </div>
          <div>
            <label class="label">响应头超时(秒)</label>
            <input v-model.number="form.timeout_sec" class="input" type="number" />
          </div>
          <div class="flex items-end pb-1.5">
            <Toggle v-model="form.enabled" label="启用" />
          </div>
        </div>
        <div>
          <label class="label">备注</label>
          <input v-model="form.note" class="input" />
        </div>
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="btn-ghost" @click="showEditor = false">取消</button>
          <button type="submit" class="btn-primary">保存</button>
        </div>
      </form>
    </Modal>
  </div>
</template>
