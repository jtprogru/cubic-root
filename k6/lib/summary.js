// Итоговая сводка прогона в терминале.

const pct = (rate) => `${(rate * 100).toFixed(2)}%`;
const ms = (value) => (value === undefined ? 'n/a' : value.toFixed(2));

// Порог плана сценария: iterations{scenario:<name>}, его ставит load.js.
const PLAN_METRIC = /^iterations\{scenario:.+\}$/;

// delivery считает, какую долю плана k6 запустил: всего и в худшем сценарии.
function delivery(m, plan) {
    const rows = Object.entries(plan).map(([name, n]) => {
        const sub = m[`iterations{scenario:${name}}`];
        return { name, n, count: sub ? sub.values.count : 0 };
    });
    if (!rows.length) {
        return null;
    }
    const total = rows.reduce((a, r) => a + r.count, 0) / rows.reduce((a, r) => a + r.n, 0);
    const worst = rows.map((r) => ({ name: r.name, ratio: r.count / r.n })).sort((a, b) => a.ratio - b.ratio)[0];
    return { total, worst, many: rows.length > 1 };
}

// verdict — одна строка, ради которой смотрят сводку: выдержал ли прогон
// пороги, и если нет, то кто упёрся: сервис или генератор нагрузки.
function verdict(failed, delivered) {
    const generator = failed.filter((name) => PLAN_METRIC.test(name));
    const slo = failed.filter((name) => !PLAN_METRIC.test(name));
    const short = delivered && `k6 started ${pct(delivered.worst.ratio)} of the plan of ${delivered.worst.name}`;
    if (!failed.length) {
        return delivered ? 'ok: thresholds held and k6 delivered the plan' : 'ok: thresholds held';
    }
    if (!slo.length) {
        return `FAIL, the load generator is the limit: ${short} (out of CPU, or out of VUs if Dropped is high)`;
    }
    if (!generator.length) {
        return `FAIL, thresholds: ${slo.join(', ')}`;
    }
    return `FAIL, thresholds: ${slo.join(', ')}; ${short} too, so compare client and server p99 on the Load test dashboard`;
}

export function textSummary(data, { profile, description, mix, plan = {} }) {
    const m = data.metrics;
    const dur = m.http_req_duration.values;
    const rps = m.http_reqs;
    const dropped = m.dropped_iterations ? m.dropped_iterations.values.count : 0;
    const wrong = m.wrong_results ? pct(m.wrong_results.values.rate) : 'n/a';
    const hits = m.cache_hits ? pct(m.cache_hits.values.rate) : 'n/a';
    const vusMax = m.vus_max ? m.vus_max.values.max : 'n/a';
    const delivered = delivery(m, plan);

    const thresholds = [];
    const failed = [];
    for (const [name, metric] of Object.entries(m)) {
        for (const [expr, result] of Object.entries(metric.thresholds || {})) {
            thresholds.push(`    ${result.ok ? 'ok  ' : 'FAIL'}  ${name}: ${expr}`);
            if (!result.ok && !failed.includes(name)) {
                failed.push(name);
            }
        }
    }
    const plannedLine = delivered
        ? `\n  Delivered (k6)  : ${pct(delivered.total)} of plan${delivered.many ? `, lowest ${delivered.worst.name} ${pct(delivered.worst.ratio)}` : ''}`
        : '';

    return `
========================================
  cubic-root load test: ${profile} (mix: ${mix})
  ${description}
========================================
  Requests total  : ${rps.values.count}
  RPS             : ${rps.values.rate.toFixed(1)}
  Max VUs         : ${vusMax}${plannedLine}
  Dropped (k6)    : ${dropped}${dropped ? '  <- the load generator could not keep up, not the service' : ''}
  Errors          : ${pct(m.errors.values.rate)}
  Wrong results   : ${wrong}
  Cache hits      : ${hits}

  Latency (ms):
    median : ${ms(dur.med)}
    p(95)  : ${ms(dur['p(95)'])}
    p(99)  : ${ms(dur['p(99)'])}
    max    : ${ms(dur.max)}

  Thresholds:
${thresholds.sort().join('\n') || '    none'}

  Verdict: ${verdict(failed.sort(), delivered)}
========================================
`;
}
