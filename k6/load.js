import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const errorRate = new Rate('errors');
const cubicRootDuration = new Trend('cubic_root_duration', true);

export const options = {
  stages: [
    { duration: '30s', target: 500 },   // разгон
    { duration: '30s', target: 1500 }, // нагрузка
    { duration: '30s', target: 2500 }, // нагрузка
    { duration: '240s', target: 3500 }, // нагрузка
    { duration: '30s', target: 0 },    // спад
  ],

  thresholds: {
    http_req_failed:      ['rate<0.01'],  // < 1% ошибок
    http_req_duration:    ['p(95)<100'],  // 95-й перцентиль < 100ms
    errors:               ['rate<0.01'],
    cubic_root_duration:  ['p(99)<200'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export default function () {
  const d = Math.random() * (1_000_000_000 - 1_000_000) + 1_000_000;
  const url = `${BASE_URL}/cubic-root?d=${d}`;

  const res = http.get(url, {
    tags: { name: 'cubic-root' },
  });

  const ok = check(res, {
    'status 200':   (r) => r.status === 200,
    'has result':   (r) => {
      try {
        return JSON.parse(r.body).result !== undefined;
      } catch {
        return false;
      }
    },
  });

  errorRate.add(!ok);
  cubicRootDuration.add(res.timings.duration);

  sleep(Math.random() * 0.05); // случайная пауза до 50ms между запросами
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

  return `
========================================
  cubic-root load test summary
========================================
  Requests total : ${rps.values.count}
  RPS            : ${rps.values.rate.toFixed(1)}
  Errors         : ${(m.errors.values.rate * 100).toFixed(2)}%

  Latency (ms):
    median : ${dur.values.med.toFixed(2)}
    p(95)  : ${dur.values['p(95)'].toFixed(2)}
    p(99)  : ${dur.values['p(99)'].toFixed(2)}
    max    : ${dur.values.max.toFixed(2)}
========================================
`;
}
