// Итоговая сводка прогона в терминале.

const pct = (rate) => `${(rate * 100).toFixed(2)}%`;
const ms = (value) => (value === undefined ? 'n/a' : value.toFixed(2));

export function textSummary(data, profile, description, mix) {
    const m = data.metrics;
    const dur = m.http_req_duration.values;
    const rps = m.http_reqs;
    const dropped = m.dropped_iterations ? m.dropped_iterations.values.count : 0;
    const wrong = m.wrong_results ? pct(m.wrong_results.values.rate) : 'n/a';
    const hits = m.cache_hits ? pct(m.cache_hits.values.rate) : 'n/a';
    const vusMax = m.vus_max ? m.vus_max.values.max : 'n/a';

    const thresholds = [];
    for (const [name, metric] of Object.entries(m)) {
        for (const [expr, result] of Object.entries(metric.thresholds || {})) {
            thresholds.push(`    ${result.ok ? 'ok  ' : 'FAIL'}  ${name}: ${expr}`);
        }
    }

    return `
========================================
  cubic-root load test: ${profile} (mix: ${mix})
  ${description}
========================================
  Requests total  : ${rps.values.count}
  RPS             : ${rps.values.rate.toFixed(1)}
  Max VUs         : ${vusMax}
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
========================================
`;
}
