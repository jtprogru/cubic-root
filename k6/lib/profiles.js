// Профили нагрузки: форма (executor k6 и его стадии), смесь трафика по
// умолчанию и пороги. Параметры переопределяются переменными окружения:
// RATE, DURATION, VUS, SPIKE_RATE, PERIOD, MAX_VUS, MIX.
//
// Большинство профилей — открытая модель (arrival-rate): k6 запускает RATE
// запросов в секунду независимо от того, как быстро отвечает сервис. В
// закрытой модели (VU + пауза) медленный сервис сам снижает себе нагрузку, и
// тест становится мягче ровно тогда, когда сервису плохо.

const env = (name, def) => (__ENV[name] !== undefined && __ENV[name] !== '' ? __ENV[name] : def);
const num = (name, def) => Number(env(name, def));

// Сколько VU держать для заданного RPS: k6 не сможет отправить запрос, если
// все VU ждут ответа, и запишет его в dropped_iterations.
const MAX_VUS = num('MAX_VUS', 3000);
const vusFor = (rate, preAllocate = 1 / 50) => ({
    preAllocatedVUs: Math.min(MAX_VUS, Math.max(10, Math.ceil(rate * preAllocate))),
    maxVUs: Math.min(MAX_VUS, Math.max(50, Math.ceil(rate / 2))),
});

// seconds переводит длительность k6 ("90s", "15m", "1h", "1h30m") в секунды.
export function seconds(duration) {
    const units = { h: 3600, m: 60, s: 1 };
    let total = 0;
    for (const [, value, unit] of String(duration).matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)) {
        total += Number(value) * units[unit];
    }
    if (!total) {
        throw new Error(`cannot parse duration "${duration}"`);
    }
    return total;
}

const s = (sec) => `${Math.round(sec)}s`;

function constantRate({ rate, duration, mix, startTime = '0s' }) {
    return {
        executor: 'constant-arrival-rate', rate, timeUnit: '1s', duration, startTime,
        ...vusFor(rate), exec: 'traffic', env: { MIX: mix },
    };
}

function rampingRate({ stages, mix, startRate = 0, startTime = '0s', preAllocate }) {
    const peak = Math.max(startRate, ...stages.map((st) => st.target));
    return {
        executor: 'ramping-arrival-rate', startRate, timeUnit: '1s', stages, startTime,
        ...vusFor(peak, preAllocate), exec: 'traffic', env: { MIX: mix },
    };
}

// rampingVUs — закрытая модель: каждый VU — пользователь, который ждёт ответ и
// думает thinkTime секунд (в среднем) перед следующим запросом.
function rampingVUs({ stages, mix, thinkTime, startTime = '0s' }) {
    return {
        executor: 'ramping-vus', startVUs: 0, stages, startTime, gracefulRampDown: '10s',
        exec: 'user', env: { MIX: mix, THINK_TIME: String(thinkTime) },
    };
}

// waves — базовый уровень и спайки до peak длиной spikeLen каждые period.
function waveStages({ base, peak, total, period, spikeLen, ramp = 10 }) {
    if (period <= spikeLen + 2 * ramp) {
        throw new Error(`PERIOD must be longer than ${spikeLen + 2 * ramp}s`);
    }
    const stages = [{ target: base, duration: s(30) }];
    let elapsed = 30;
    while (elapsed + period <= total) {
        stages.push(
            { target: base, duration: s(period - spikeLen - 2 * ramp) },
            { target: peak, duration: s(ramp) },
            { target: peak, duration: s(spikeLen) },
            { target: base, duration: s(ramp) },
        );
        elapsed += period;
    }
    if (total - elapsed > 0) {
        stages.push({ target: base, duration: s(total - elapsed) });
    }
    stages.push({ target: 0, duration: s(10) });
    return stages;
}

// Пороги: wrong_results и checks обязательны всегда, остальные зависят от профиля.
const correctness = {
    wrong_results: ['rate<0.001'],
    checks: ['rate>0.99'],
};
const healthy = {
    ...correctness,
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<100', 'p(99)<250'],
};

export const PROFILES = {
    // smoke — 30 секунд лёгкого смешанного трафика: сервис жив, мониторинг
    // видит все виды запросов, пороги строгие.
    smoke: () => ({
        description: '30s at 50 req/s of mixed traffic, strict thresholds',
        mix: 'realistic',
        scenarios: (mix) => ({ smoke: constantRate({ rate: num('RATE', 50), duration: env('DURATION', '30s'), mix }) }),
        thresholds: healthy,
    }),

    // demo — 3 минуты, после которых на дашбордах есть всё: прогрев кэша,
    // стабильная нагрузка, короткий спайк с saturation и восстановление.
    demo: () => {
        const rate = num('RATE', 300);
        const peak = num('SPIKE_RATE', rate * 15);
        return {
            description: `~3m: warm-up, ${rate} req/s, spike to ${peak} req/s, recovery`,
            mix: 'realistic',
            scenarios: (mix) => ({
                demo: rampingRate({
                    mix,
                    stages: [
                        { target: rate, duration: '20s' },
                        { target: rate, duration: '60s' },
                        { target: peak, duration: '10s' },
                        { target: peak, duration: '20s' },
                        { target: rate, duration: '10s' },
                        { target: rate, duration: '50s' },
                        { target: 0, duration: '10s' },
                    ],
                }),
            }),
            thresholds: correctness,
        };
    },

    // baseline — закрытая модель, как раньше: VU с паузой 0–50ms, разгон до 4500 VU.
    baseline: () => ({
        description: 'closed model: ramp to 4500 VUs with 0-50ms think time',
        mix: 'uniform',
        scenarios: (mix) => ({
            baseline: rampingVUs({
                mix,
                thinkTime: 0.025,
                stages: [
                    { duration: '30s', target: 500 },  // разгон
                    { duration: '30s', target: 1500 }, // нагрузка
                    { duration: '30s', target: 2500 }, // нагрузка
                    { duration: '240s', target: 3500 }, // нагрузка
                    { duration: '30s', target: 4500 }, // нагрузка
                    { duration: '30s', target: 0 },    // спад
                ],
            }),
        }),
        thresholds: { ...healthy },
    }),

    // load — постоянный RATE в течение DURATION: «обычный день» сервиса.
    load: () => {
        const rate = num('RATE', 2000);
        return {
            description: `${rate} req/s for ${env('DURATION', '10m')}`,
            mix: 'realistic',
            scenarios: (mix) => ({ load: constantRate({ rate, duration: env('DURATION', '10m'), mix }) }),
            thresholds: healthy,
        };
    },

    // stress — ступени RATE×0.5…×4 по STEP: где начинается деградация.
    stress: () => {
        const rate = num('RATE', 1500);
        const step = env('STEP', '2m');
        const stages = [];
        for (const k of [0.5, 1, 2, 3, 4]) {
            stages.push({ target: rate * k, duration: '20s' }, { target: rate * k, duration: step });
        }
        stages.push({ target: 0, duration: '20s' });
        return {
            description: `steps from ${rate * 0.5} to ${rate * 4} req/s, ${step} each`,
            mix: 'realistic',
            scenarios: (mix) => ({ stress: rampingRate({ mix, stages }) }),
            thresholds: correctness,
        };
    },

    // spike — с RATE до SPIKE_RATE за 10 секунд, минута на пике, восстановление.
    spike: () => {
        const rate = num('RATE', 500);
        const peak = num('SPIKE_RATE', 6000);
        return {
            description: `${rate} req/s, spike to ${peak} req/s for 1m, recovery`,
            mix: 'uniform',
            scenarios: (mix) => ({
                spike: rampingRate({
                    mix,
                    startRate: rate,
                    stages: [
                        { target: rate, duration: '40s' },
                        { target: peak, duration: '10s' },
                        { target: peak, duration: '1m' },
                        { target: rate, duration: '10s' },
                        { target: rate, duration: '1m' },
                        { target: 0, duration: '10s' },
                    ],
                }),
            }),
            thresholds: correctness,
        };
    },

    // soak — долгая ровная нагрузка: утечки, рост памяти, GC, TTL кэша.
    soak: () => {
        const rate = num('RATE', 1000);
        return {
            description: `${rate} req/s for ${env('DURATION', '1h')}`,
            mix: 'realistic',
            scenarios: (mix) => ({ soak: constantRate({ rate, duration: env('DURATION', '1h'), mix }) }),
            thresholds: healthy,
        };
    },

    // waves — долгая нагрузка со спайками каждые PERIOD: как система переживает
    // повторяющиеся всплески и успевает ли восстанавливаться между ними.
    waves: () => {
        const rate = num('RATE', 1000);
        const peak = num('SPIKE_RATE', rate * 6);
        const total = seconds(env('DURATION', '30m'));
        const period = seconds(env('PERIOD', '5m'));
        return {
            description: `${rate} req/s with spikes to ${peak} req/s every ${env('PERIOD', '5m')} for ${env('DURATION', '30m')}`,
            mix: 'realistic',
            scenarios: (mix) => ({
                waves: rampingRate({
                    mix, startRate: rate,
                    stages: waveStages({ base: rate, peak, total, period, spikeLen: 30 }),
                }),
            }),
            thresholds: correctness,
        };
    },

    // breakpoint — RPS растёт до MAX_RATE, пока задержка или ошибки не превысят
    // порог: тогда тест останавливается. Последний RPS до остановки — предел.
    // Быстрый режим: тела ответов не читаются, проверяется только статус, VU
    // выделяются заранее. Если тест остановился по dropped_iterations, предел
    // нашёлся у генератора нагрузки, а не у сервиса.
    breakpoint: () => {
        const max = num('MAX_RATE', 40000);
        return {
            description: `ramp up to ${max} req/s over ${env('DURATION', '10m')}, abort on degradation (status-only checks)`,
            mix: 'uniform',
            options: { discardResponseBodies: true },
            scenarios: (mix) => ({
                breakpoint: rampingRate({
                    mix, startRate: 100, preAllocate: 1 / 20,
                    stages: [{ target: max, duration: env('DURATION', '10m') }],
                }),
            }),
            thresholds: {
                ...correctness,
                http_req_failed: [{ threshold: 'rate<0.05', abortOnFail: true, delayAbortEval: '30s' }],
                http_req_duration: [{ threshold: 'p(99)<250', abortOnFail: true, delayAbortEval: '30s' }],
                dropped_iterations: [{ threshold: 'count<1000', abortOnFail: true, delayAbortEval: '30s' }],
            },
        };
    },

    // marathon — час смешанной нагрузки из нескольких одновременных сценариев:
    // фоновый трафик весь час, сверху по очереди ступени stress, волна спайков
    // и толпа пользователей в закрытой модели, плюс постоянные струйки
    // невалидных запросов и экзотических входов. Пороги по сценариям показывают,
    // как спайки и толпа задевают фоновый трафик.
    marathon: () => {
        const rate = num('RATE', 1000);
        const peak = num('SPIKE_RATE', rate * 6);
        const vus = num('VUS', 3000);
        return {
            description: `1h: background ${rate} req/s + stress, spikes to ${peak} req/s and ${vus} closed-model users`,
            mix: 'realistic',
            scenarios: (mix) => ({
                background: constantRate({ rate, duration: '60m', mix }),
                invalid: constantRate({ rate: 10, duration: '60m', mix: 'bad-requests' }),
                exotic: constantRate({ rate: 20, duration: '60m', mix: 'wide-range' }),
                stress: rampingRate({
                    mix: 'uniform', startTime: '10m',
                    stages: [
                        { target: rate, duration: '2m' }, { target: rate, duration: '2m' },
                        { target: rate * 2, duration: '1m' }, { target: rate * 2, duration: '2m' },
                        { target: rate * 3, duration: '1m' }, { target: rate * 3, duration: '2m' },
                        { target: 0, duration: '30s' },
                    ],
                }),
                spikes: rampingRate({
                    mix: 'hot-keys', startTime: '22m',
                    stages: waveStages({ base: 0, peak, total: 15 * 60, period: 180, spikeLen: 20 }),
                }),
                crowd: rampingVUs({
                    mix, startTime: '40m', thinkTime: 1,
                    stages: [
                        { duration: '3m', target: vus },
                        { duration: '8m', target: vus },
                        { duration: '1m', target: 0 },
                    ],
                }),
            }),
            thresholds: {
                ...correctness,
                'http_req_duration{scenario:background}': ['p(99)<100'],
                'http_req_failed{scenario:background}': ['rate<0.01'],
                'http_req_duration{scenario:crowd}': ['p(99)<250'],
            },
        };
    },
};
