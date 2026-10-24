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
//   k6 run -e SCENARIO=marathon -e RATE=3000 -e FAST=1 k6/load.js
import { sleep } from 'k6';
import { MIXES, send } from './lib/traffic.js';
import { PROFILES, planned } from './lib/profiles.js';
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

const scenarios = profile.scenarios(MIX);

// План по каждому arrival-rate сценарию. Когда генератору не хватает CPU, k6
// отстаёт от расписания и теряет итерации молча, dropped_iterations этого не
// видит. Порог «запущено не меньше DELIVERED плана» ловит такой прогон: предел
// в нём нашёлся у генератора, а не у сервиса. Профили, которые сами
// останавливаются по порогу (breakpoint), план заведомо не выполняют.
const DELIVERED = 0.98;
const aborts = Object.values(profile.thresholds).flat().some((t) => t.abortOnFail);
const plan = {};
if (!aborts) {
    for (const [name, scenario] of Object.entries(scenarios)) {
        const n = planned(scenario);
        if (n) {
            plan[name] = Math.round(n);
        }
    }
}
const delivery = Object.fromEntries(
    Object.entries(plan).map(([name, n]) => [`iterations{scenario:${name}}`, [`count>=${Math.floor(n * DELIVERED)}`]]),
);

// FAST=1 — быстрый режим для любого профиля: тела ответов не читаются,
// проверяется только статус. Запрос стоит генератору меньше CPU, но
// правильность результатов не проверяется.
const FAST = ['1', 'true'].includes(__ENV.FAST);

export const options = {
    ...(profile.options || {}),
    ...(FAST ? { discardResponseBodies: true } : {}),
    scenarios,
    thresholds: { ...profile.thresholds, ...delivery },
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
    return {
        stdout: textSummary(data, {
            profile: NAME, description: profile.description, mix: MIX, plan,
            statusOnly: Boolean(options.discardResponseBodies),
        }),
    };
}
