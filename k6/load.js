import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// SCENARIO выбирает профиль нагрузки и распределение входов:
//   baseline      — равномерное d в [1e6, 1e9], разгон до 3500 VU;
//   wide-range    — log-uniform |d| в [1e-20, 1e40] с обоими знаками: входы,
//                   на которых видно, сходится ли алгоритм и насколько точен ответ;
//   bad-requests  — четверть запросов невалидные: 4xx и 405 в HTTP-метриках,
//                   cubic_root_validation_errors_total по причинам;
//   spike         — резкий скачок с 50 до 3000 VU: saturation и восстановление.
const SCENARIO = __ENV.SCENARIO || 'baseline';
const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

const short = [
  { duration: '30s', target: 50 },
  { duration: '2m', target: 50 },
  { duration: '15s', target: 0 },
];

const scenarios = {
  baseline: {
    stages: [
      { duration: '30s', target: 500 },  // разгон
      { duration: '30s', target: 1500 }, // нагрузка
      { duration: '30s', target: 2500 }, // нагрузка
      { duration: '240s', target: 3500 }, // нагрузка
      { duration: '30s', target: 0 },    // спад
    ],
    request: () => validRequest(uniform(1_000_000, 1_000_000_000)),
  },
  'wide-range': {
    stages: short,
    request: () => validRequest(logUniform(-20, 40)),
  },
  'bad-requests': {
    stages: short,
    request: () => (Math.random() < 0.25 ? badRequest() : validRequest(uniform(1_000_000, 1_000_000_000))),
  },
  spike: {
    stages: [
      { duration: '10s', target: 50 },
      { duration: '30s', target: 50 },
      { duration: '10s', target: 3000 }, // скачок
      { duration: '1m', target: 3000 },
      { duration: '10s', target: 50 },   // спад
      { duration: '30s', target: 50 },   // восстановление
      { duration: '10s', target: 0 },
    ],
    request: () => validRequest(uniform(1_000_000, 1_000_000_000)),
  },
};

const scenario = scenarios[SCENARIO];
if (!scenario) {
  throw new Error(`unknown SCENARIO=${SCENARIO}, expected one of: ${Object.keys(scenarios).join(', ')}`);
}

// errors — доля запросов, не прошедших проверки: неожиданный статус или
// неверный результат. wrong_results — только неверные результаты 200-ответов.
const errorRate = new Rate('errors');
const wrongResults = new Rate('wrong_results');
const cubicRootDuration = new Trend('cubic_root_duration', true);

// 400 и 405 в bad-requests ожидаемы и не должны считаться отказами.
http.setResponseCallback(http.expectedStatuses(200, 400, 405));

export const options = {
  stages: scenario.stages,
  // По умолчанию k6 не считает p(99), а итоговая сводка ниже его выводит.
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],

  thresholds: {
    http_req_failed:      ['rate<0.01'],  // < 1% ошибок
    http_req_duration:    ['p(95)<100'],  // 95-й перцентиль < 100ms
    errors:               ['rate<0.01'],
    wrong_results:        ['rate<0.001'],
    cubic_root_duration:  ['p(99)<200'],
  },
};

export default function () {
  const ok = scenario.request();
  errorRate.add(!ok);
  sleep(Math.random() * 0.05); // случайная пауза до 50ms между запросами
}

function uniform(min, max) {
  return Math.random() * (max - min) + min;
}

function logUniform(minExp, maxExp) {
  const sign = Math.random() < 0.5 ? -1 : 1;
  return sign * Math.pow(10, uniform(minExp, maxExp));
}

// validRequest отправляет корректный d и сверяет ответ с Math.cbrt.
function validRequest(d) {
  // encodeURIComponent: JS пишет большие числа как 1.2e+25, а "+" в query string — это пробел.
  const res = http.get(`${BASE_URL}/cubic-root?d=${encodeURIComponent(d)}`, { tags: { name: 'cubic-root' } });
  cubicRootDuration.add(res.timings.duration);

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

export function handleSummary(data) {
  return {
    stdout: textSummary(data),
  };
}

function textSummary(data) {
  const m = data.metrics;
  const dur = m.http_req_duration;
  const rps = m.http_reqs;
  const wrong = m.wrong_results ? (m.wrong_results.values.rate * 100).toFixed(2) : '0.00';

  return `
========================================
  cubic-root load test summary (${SCENARIO})
========================================
  Requests total : ${rps.values.count}
  RPS            : ${rps.values.rate.toFixed(1)}
  Errors         : ${(m.errors.values.rate * 100).toFixed(2)}%
  Wrong results  : ${wrong}%

  Latency (ms):
    median : ${dur.values.med.toFixed(2)}
    p(95)  : ${dur.values['p(95)'].toFixed(2)}
    p(99)  : ${dur.values['p(99)'].toFixed(2)}
    max    : ${dur.values.max.toFixed(2)}
========================================
`;
}
