// Трафик: какие запросы отправлять и как проверять ответы.
//
// Смесь (MIX) задаёт доли видов запросов и не зависит от формы нагрузки:
// один и тот же спайк можно гонять и по горячим ключам, и по невалидным входам.
import http from 'k6/http';
import { check } from 'k6';
import { Rate, Trend } from 'k6/metrics';

export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// errors — доля итераций, не прошедших проверки: неожиданный статус или
// неверный результат. wrong_results — только неверные результаты 200-ответов.
export const errorRate = new Rate('errors');
const wrongResults = new Rate('wrong_results');
const cubicRootDuration = new Trend('cubic_root_duration', true);
// cache_hits — доля ответов из кэша по заголовку X-Cache: клиентская сверка
// с серверной метрикой cubic_root_cache_requests_total.
const cacheHits = new Rate('cache_hits');

// 400 и 405 на невалидные запросы ожидаемы и не должны считаться отказами.
http.setResponseCallback(http.expectedStatuses(200, 400, 405));

const HOT_KEYS = 1000;

// Генераторы значения d для валидных запросов.
const inputs = {
    // uniform — d равномерно в [1e6, 1e9]: каждый запрос уникален, кэш не помогает.
    uniform: () => uniform(1_000_000, 1_000_000_000),
    // hot — одно из HOT_KEYS целых значений: кэш попадает почти всегда.
    hot: () => (1 + Math.floor(Math.random() * HOT_KEYS)) * 1_000_000,
    // wide — log-uniform |d| в [1e-20, 1e40] с обоими знаками: проверка точности
    // алгоритма на краях диапазона.
    wide: () => {
        const sign = Math.random() < 0.5 ? -1 : 1;
        return sign * Math.pow(10, uniform(-20, 40));
    },
};

// Смеси трафика: доли видов запросов. bad — невалидный d или POST.
export const MIXES = {
    uniform: { uniform: 1 },
    'hot-keys': { hot: 0.8, uniform: 0.2 },
    'wide-range': { wide: 1 },
    'bad-requests': { uniform: 0.75, bad: 0.25 },
    // realistic — то, что видит живой сервис: в основном повторяющиеся запросы,
    // заметная доля уникальных и немного мусора и экзотики.
    realistic: { hot: 0.6, uniform: 0.3, wide: 0.05, bad: 0.05 },
};

const pickers = {};

// picker возвращает функцию, выбирающую вид запроса по весам смеси.
function picker(mix) {
    if (!pickers[mix]) {
        const weights = MIXES[mix];
        if (!weights) {
            throw new Error(`unknown MIX=${mix}, expected one of: ${Object.keys(MIXES).join(', ')}`);
        }
        const total = Object.values(weights).reduce((a, b) => a + b, 0);
        let acc = 0;
        const table = Object.entries(weights).map(([kind, w]) => {
            acc += w / total;
            return [kind, acc];
        });
        pickers[mix] = () => {
            const u = Math.random();
            return (table.find(([, edge]) => u < edge) || table[table.length - 1])[0];
        };
    }
    return pickers[mix];
}

// send отправляет один запрос по смеси mix и возвращает, прошёл ли он проверки.
export function send(mix) {
    const kind = picker(mix)();
    const ok = kind === 'bad' ? badRequest() : validRequest(inputs[kind]());
    errorRate.add(!ok);
    return ok;
}

function uniform(min, max) {
    return Math.random() * (max - min) + min;
}

// validRequest отправляет корректный d и сверяет ответ с Math.cbrt.
function validRequest(d) {
    // encodeURIComponent: JS пишет большие числа как 1.2e+25, а "+" в query string — это пробел.
    const res = http.get(`${BASE_URL}/cubic-root?d=${encodeURIComponent(d)}`, { tags: { name: 'cubic-root' } });
    cubicRootDuration.add(res.timings.duration);

    // С discardResponseBodies тело не читается: быстрый режим проверяет только статус.
    if (res.body === null) {
        return check(res, { 'status 200': (r) => r.status === 200 });
    }

    let result;
    try {
        result = JSON.parse(res.body).result;
    } catch {
        result = undefined;
    }
    const expected = Math.cbrt(d);
    const correct = result !== undefined && Math.abs(result - expected) <= 1e-9 * Math.abs(expected);
    if (res.status === 200) {
        wrongResults.add(!correct);
    }

    if (res.headers['X-Cache']) {
        cacheHits.add(res.headers['X-Cache'] === 'HIT');
    }

    return check(res, {
        'status 200': (r) => r.status === 200,
        'has result': () => result !== undefined,
        'correct result': () => correct,
    });
}

const badInputs = [
    { query: 'd=abc', status: 400 },
    { query: '', status: 400 },
    { query: 'd=NaN', status: 400 },
    { query: 'd=1e400', status: 400 },
];

// badRequest отправляет невалидный d или неподдерживаемый метод.
function badRequest() {
    if (Math.random() < 0.2) {
        const res = http.post(`${BASE_URL}/cubic-root?d=27`, null, { tags: { name: 'cubic-root-post' } });
        return check(res, { 'status 405': (r) => r.status === 405 });
    }
    const input = badInputs[Math.floor(Math.random() * badInputs.length)];
    const res = http.get(`${BASE_URL}/cubic-root?${input.query}`, { tags: { name: 'cubic-root-bad' } });
    return check(res, { [`status ${input.status}`]: (r) => r.status === input.status });
}
