// Нагрузочные тесты cubic-root.
//
// SCENARIO выбирает форму нагрузки, MIX — смесь трафика (по умолчанию своя у
// каждого профиля). Профили описаны в lib/profiles.js, смеси — в lib/traffic.js.
//
//   быстрые:   smoke (30s), demo (~3m, зажигает все дашборды)
//   форма:     load, stress, spike, breakpoint, baseline (закрытая модель)
//   длинные:   soak (1h ровно), waves (30m со спайками), marathon (1h, всё сразу)
//   смеси:     uniform, hot-keys, wide-range, bad-requests, realistic
//
// Примеры:
//   k6 run -e SCENARIO=demo k6/load.js
//   k6 run -e SCENARIO=spike -e MIX=hot-keys -e SPIKE_RATE=20000 k6/load.js
//   k6 run -e SCENARIO=soak -e RATE=2000 -e DURATION=2h k6/load.js
import { sleep } from 'k6';
import { MIXES, send } from './lib/traffic.js';
import { PROFILES } from './lib/profiles.js';
import { textSummary } from './lib/summary.js';

const NAME = __ENV.SCENARIO || 'baseline';
const build = PROFILES[NAME];
if (!build) {
    throw new Error(`unknown SCENARIO=${NAME}, expected one of: ${Object.keys(PROFILES).join(', ')}`);
}
const profile = build();
const MIX = __ENV.MIX || profile.mix;
if (!MIXES[MIX]) {
    throw new Error(`unknown MIX=${MIX}, expected one of: ${Object.keys(MIXES).join(', ')}`);
}

export const options = {
    ...(profile.options || {}),
    scenarios: profile.scenarios(MIX),
    thresholds: profile.thresholds,
    // По умолчанию k6 не считает p(99), а итоговая сводка его выводит.
    summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
};

// traffic — итерация открытой модели: один запрос, без пауз. Темп задаёт executor.
export function traffic() {
    send(__ENV.MIX);
}

// user — итерация закрытой модели: запрос и пауза «на подумать» со средним
// THINK_TIME секунд.
export function user() {
    send(__ENV.MIX);
    sleep(Math.random() * 2 * Number(__ENV.THINK_TIME));
}

export function handleSummary(data) {
    return { stdout: textSummary(data, NAME, profile.description, MIX) };
}
