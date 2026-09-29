<script lang="ts">
import { defineComponent, onMounted, onBeforeUnmount, ref, watch } from 'vue';
import * as echarts from 'echarts/core';
import { LineChart, BarChart, PieChart } from 'echarts/charts';
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import { theme } from '../theme';

echarts.use([LineChart, BarChart, PieChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer]);

export default defineComponent({
  name: 'LineChart',
  props: {
    option: { type: Object, required: true },
    height: { type: String, default: '300px' },
  },
  setup(props) {
    const el = ref<HTMLDivElement | null>(null);
    let chart: echarts.ECharts | null = null;
    let ro: ResizeObserver | null = null;

    const render = (): void => {
      if (!el.value) return;
      if (!chart) {
        chart = echarts.init(el.value, theme.dark ? 'dark' : undefined);
      } else {
        chart.dispose();
        chart = echarts.init(el.value, theme.dark ? 'dark' : undefined);
      }
      chart.setOption(props.option);
    };

    onMounted(() => {
      render();
      ro = new ResizeObserver(() => chart && chart.resize());
      if (el.value) ro.observe(el.value);
    });
    watch(() => props.option, render, { deep: true });
    watch(() => theme.dark, render);

    onBeforeUnmount(() => {
      ro?.disconnect();
      chart?.dispose();
      chart = null;
    });
    return { el };
  },
});
</script>

<template>
  <div ref="el" :style="{ height, width: '100%' }" />
</template>
