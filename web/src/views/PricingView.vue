<script lang="ts">
import { defineComponent } from 'vue';
import { api, Price, Tier } from '../api';
import { toast, confirmDialog } from '../toast';
import Modal from '../components/Modal.vue';
import Toggle from '../components/Toggle.vue';
import IconPlus from '~icons/lucide/plus';
import IconTrash from '~icons/lucide/trash-2';
import IconUpload from '~icons/lucide/upload';
import IconSun from '~icons/lucide/sun';
import IconMoon from '~icons/lucide/moon';

function emptyTier(): Tier {
  return { enabled: false, start: '09:00', end: '12:00', mode: 'multiplier', rate: 1.5, input: 0, output: 0, cache_read: 0, cache_creation: 0 };
}

export default defineComponent({
  name: 'PricingView',
  components: { Modal, Toggle, IconPlus, IconTrash, IconUpload, IconSun, IconMoon },
  data() {
    return {
      prices: [] as Price[],
      showEditor: false,
      showImport: false,
      importText: '',
      form: {
        model: '',
        input: 0, output: 0, cache_read: 0, cache_creation: 0,
        peak: emptyTier(),
        valley: emptyTier(),
      },
    };
  },
  mounted() {
    this.load();
  },
  methods: {
    load(): void {
      api.pricing().then((p) => { this.prices = p || []; }).catch((e) => toast('error', e.message));
    },
    edit(p?: Price): void {
      this.form = p
        ? {
            model: p.model,
            input: p.input, output: p.output, cache_read: p.cache_read, cache_creation: p.cache_creation,
            peak: p.peak ? { ...emptyTier(), ...p.peak } : emptyTier(),
            valley: p.valley ? { ...emptyTier(), ...p.valley } : emptyTier(),
          }
        : { model: '', input: 0, output: 0, cache_read: 0, cache_creation: 0, peak: emptyTier(), valley: emptyTier() };
      this.showEditor = true;
    },
    async save(): Promise<void> {
      if (!this.form.model.trim()) { toast('error', '模型名不能为空'); return; }
      const payload: Price = {
        model: this.form.model,
        input: this.form.input, output: this.form.output,
        cache_read: this.form.cache_read, cache_creation: this.form.cache_creation,
        peak: this.form.peak.enabled ? this.form.peak : null,
        valley: this.form.valley.enabled ? this.form.valley : null,
      };
      api.upsertPrice(payload).then(() => { this.showEditor = false; this.load(); }).catch((e) => toast('error', e.message));
    },
    async remove(p: Price): Promise<void> {
      const okGo = await confirmDialog(`删除「${p.model}」的定价?`, '删除定价', '删除');
      if (!okGo) return;
      try {
        await api.deletePrice(p.model);
        toast('success', `「${p.model}」定价已删除`);
        this.load();
      } catch (e) { toast('error', (e as Error).message); }
    },
    doImport(): void {
      api.importPricing(this.importText)
        .then((r) => { toast('success', `成功导入 ${r.imported} 条定价`); this.showImport = false; this.load(); })
        .catch((e) => toast('error', e.message));
    },
    tierSummary(p: Price): string {
      const parts: string[] = [];
      const fmt = (name: string, t?: Tier | null): void => {
        if (!t || !t.enabled) return;
        const price = t.mode === 'multiplier' ? `×${t.rate}` : `${t.input}/${t.output}`;
        parts.push(`${name} ${t.start}–${t.end} ${price}`);
      };
      fmt('峰', p.peak);
      fmt('谷', p.valley);
      return parts.join('  ');
    },
    flatSet(): boolean {
      return !!(this.form.input || this.form.output || this.form.cache_read || this.form.cache_creation);
    },
  },
});
</script>

<template>
  <div>
    <div class="mb-5 flex items-center justify-between">
      <h1 class="text-xl font-bold">定价</h1>
      <div class="flex gap-2">
        <button class="btn-ghost" @click="showImport = true"><IconUpload class="h-4 w-4" /> 导入 pricing.json</button>
        <button class="btn-primary" @click="edit()"><IconPlus class="h-4 w-4" /> 添加定价</button>
      </div>
    </div>

    <div class="card overflow-x-auto">
      <table class="w-full min-w-[760px]">
        <thead>
          <tr class="border-b border-slate-100 dark:border-slate-800">
            <th class="th">模型</th>
            <th class="th">平价 输入</th>
            <th class="th">平价 输出</th>
            <th class="th">平价 缓存读</th>
            <th class="th">平价 缓存写</th>
            <th class="th">峰谷</th>
            <th class="th w-20"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!prices.length"><td colspan="7" class="td py-10 text-center text-slate-400">暂无定价。导入或手动添加。</td></tr>
          <tr v-for="p in prices" :key="p.model" class="border-b border-slate-50 dark:border-slate-800/60">
            <td class="td font-mono text-xs font-medium">{{ p.model }}</td>
            <td class="td">{{ p.input || '—' }}</td>
            <td class="td">{{ p.output || '—' }}</td>
            <td class="td">{{ p.cache_read || '—' }}</td>
            <td class="td">{{ p.cache_creation || '—' }}</td>
            <td class="td">
              <span v-if="tierSummary(p)" class="font-mono text-[11px]">
                <IconSun v-if="p.peak?.enabled" class="mr-0.5 inline h-3.5 w-3.5 -mt-0.5 text-amber-500" /><template v-if="p.peak?.enabled">峰</template>
                <IconMoon v-if="p.valley?.enabled" class="ml-1.5 mr-0.5 inline h-3.5 w-3.5 -mt-0.5 text-indigo-500" /><template v-if="p.valley?.enabled">谷</template>
                <span class="ml-1 text-slate-500 dark:text-slate-400">{{ tierSummary(p) }}</span>
              </span>
              <span v-else class="text-slate-300 dark:text-slate-600">—</span>
            </td>
            <td class="td text-right">
              <button class="btn-ghost !px-2 text-xs" @click="edit(p)">编辑</button>
              <button class="btn-danger !px-2" @click="remove(p)"><IconTrash class="h-4 w-4" /></button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <Modal :open="showEditor" title="模型定价" wide @close="showEditor = false">
      <form class="space-y-4" @submit.prevent="save">
        <div class="flex items-end gap-3">
          <div class="flex-1">
            <label class="label">上游模型名 *</label>
            <input v-model="form.model" class="input font-mono text-xs" placeholder="如 kimi-for-coding[1M]" required />
          </div>
        </div>

        <!-- 平价 -->
        <div class="rounded-xl bg-slate-50 p-3 dark:bg-slate-950/50">
          <div class="mb-2 flex items-baseline justify-between">
            <span class="text-sm font-semibold">平价</span>
            <span class="text-[11px] text-slate-400">$/百万 token · 可选: 峰谷时段之外按此计费, 未填则峰谷外不计费</span>
          </div>
          <div class="grid grid-cols-4 gap-2">
            <div><label class="label">输入</label><input v-model.number="form.input" class="input text-xs" type="number" step="any" /></div>
            <div><label class="label">输出</label><input v-model.number="form.output" class="input text-xs" type="number" step="any" /></div>
            <div><label class="label">缓存读</label><input v-model.number="form.cache_read" class="input text-xs" type="number" step="any" /></div>
            <div><label class="label">缓存写</label><input v-model.number="form.cache_creation" class="input text-xs" type="number" step="any" /></div>
          </div>
        </div>

        <!-- 峰 / 谷 -->
        <div v-for="(tier, key) in { peak: form.peak, valley: form.valley }" :key="key"
             class="rounded-xl border p-3"
             :class="tier.enabled ? 'border-slate-300 dark:border-slate-600' : 'border-dashed border-slate-200 dark:border-slate-800'">
          <div class="mb-2 flex items-center justify-between">
            <span class="flex items-center gap-1.5 text-sm font-semibold">
              <IconSun v-if="key === 'peak'" class="h-4 w-4 text-amber-500" />
              <IconMoon v-else class="h-4 w-4 text-indigo-500" />
              {{ key === 'peak' ? '峰时段' : '谷时段' }}
            </span>
            <Toggle v-model="tier.enabled" label="启用" />
          </div>
          <template v-if="tier.enabled">
            <div class="mb-2 flex flex-wrap items-center gap-3">
              <label class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400">
                开始
                <input v-model="tier.start" class="input !w-28 text-xs" type="time" />
              </label>
              <label class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400">
                结束
                <input v-model="tier.end" class="input !w-28 text-xs" type="time" />
              </label>
              <span class="text-[11px] text-slate-400">支持跨午夜, 如 23:00–07:00</span>
            </div>
            <div class="flex flex-wrap items-center gap-4">
              <label class="flex items-center gap-1.5 text-xs">
                <input type="radio" :value="'absolute'" v-model="tier.mode" /> 绝对价格
              </label>
              <label class="flex items-center gap-1.5 text-xs">
                <input type="radio" :value="'multiplier'" v-model="tier.mode" /> 平价倍率
              </label>
            </div>
            <div v-if="tier.mode === 'absolute'" class="mt-2 grid grid-cols-4 gap-2">
              <div><label class="label">输入</label><input v-model.number="tier.input" class="input text-xs" type="number" step="any" /></div>
              <div><label class="label">输出</label><input v-model.number="tier.output" class="input text-xs" type="number" step="any" /></div>
              <div><label class="label">缓存读</label><input v-model.number="tier.cache_read" class="input text-xs" type="number" step="any" /></div>
              <div><label class="label">缓存写</label><input v-model.number="tier.cache_creation" class="input text-xs" type="number" step="any" /></div>
            </div>
            <div v-else class="mt-2 flex items-center gap-3">
              <label class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
                倍率
                <input v-model.number="tier.rate" class="input !w-24 text-xs" type="number" step="any" min="0" />
              </label>
              <span class="text-[11px]" :class="flatSet() ? 'text-slate-400' : 'text-rose-500'">
                该档价格 = 平价 × 倍率<span v-if="!flatSet()">, 需先填写平价</span>
              </span>
            </div>
          </template>
        </div>

        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="btn-ghost" @click="showEditor = false">取消</button>
          <button type="submit" class="btn-primary">保存</button>
        </div>
      </form>
    </Modal>

    <Modal :open="showImport" title="导入 cc-switch model-pricing.json" @close="showImport = false">
      <div class="space-y-3">
        <p class="text-xs text-slate-400">粘贴 %USERPROFILE%\.cc-switch\model-pricing.json 文件内容, 已存在的模型会被覆盖。</p>
        <textarea v-model="importText" class="input h-48 font-mono text-[11px]" placeholder='{"version":1,"models":[{"modelId":"...","inputCostPerMillion":"2",...}]}' />
        <div class="flex justify-end gap-2">
          <button class="btn-ghost" @click="showImport = false">取消</button>
          <button class="btn-primary" @click="doImport">导入</button>
        </div>
      </div>
    </Modal>
  </div>
</template>
