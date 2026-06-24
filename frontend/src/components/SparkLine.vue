<script setup lang="ts">
import { computed } from 'vue';

export type TrendSample = {
  value: number;
  status: string;
  checkedAt: string;
};

const props = defineProps<{
  samples?: TrendSample[];
  refreshing?: boolean;
  max?: number;
}>();

const bars = computed(() => {
  const history = (props.samples ?? []).slice(-(props.max ?? 60));
  const max = Math.max(...history.map((sample) => sample.value), 1);
  return history.map((sample) => {
    const height = Math.max(14, Math.round((sample.value / max) * 34));
    return {
      ...sample,
      height,
      className: sample.status === 'available' ? 'ok' : sample.status === 'unknown' ? 'idle' : 'bad',
    };
  });
});

const emptyBars = computed(() => Array.from({ length: 24 }, (_, index) => ({ height: 12 + (index % 5) * 4 })));
</script>

<template>
  <div class="bar-trend" :class="{ refreshing }" role="img" aria-label="最近检测历史">
    <div class="bar-trend-meta">
      <span>近 {{ bars.length || 0 }} 次记录</span>
      <span v-if="refreshing">刷新中</span>
    </div>
    <div v-if="bars.length" class="bar-track">
      <i
        v-for="(bar, index) in bars"
        :key="`${bar.checkedAt}-${index}`"
        :class="bar.className"
        :style="{ height: `${bar.height}px` }"
        :title="`${bar.value} ms · ${bar.status}`"
      ></i>
    </div>
    <div v-else class="bar-track empty">
      <i v-for="(bar, index) in emptyBars" :key="index" :style="{ height: `${bar.height}px` }"></i>
    </div>
    <div class="bar-trend-axis">
      <span>PAST</span>
      <span>NOW</span>
    </div>
  </div>
</template>
