import http from 'k6/http';
import exec from 'k6/execution';

const targets = __ENV.TARGETS.split(',');
const rate = parseInt(__ENV.RATE || '2000', 10);
const duration = __ENV.DURATION || '20s';
const keyCount = 1000;

http.setResponseCallback(http.expectedStatuses(200));

export const options = {
  summaryTrendStats: ['p(50)', 'p(90)', 'p(99)', 'max'],
  scenarios: {
    ramp: {
      executor: 'constant-arrival-rate',
      rate: rate,
      timeUnit: '1s',
      duration: duration,
      preAllocatedVUs: 400,
      maxVUs: 3000,
    },
  },
};

export default function () {
  const iteration = exec.scenario.iterationInTest;
  const target = targets[iteration % targets.length];
  const apiKey = `bench-${Math.floor(iteration / targets.length) % keyCount}`;
  http.get(`${target}/bench/resource`, { headers: { 'X-API-Key': apiKey } });
}

function valuesOf(data, name) {
  const metric = data.metrics[name];
  if (!metric) {
    return null;
  }
  return metric.values;
}

export function handleSummary(data) {
  const result = {
    offered: rate,
    duration: valuesOf(data, 'http_req_duration'),
    requests: valuesOf(data, 'http_reqs'),
    failed: valuesOf(data, 'http_req_failed'),
    dropped: valuesOf(data, 'dropped_iterations'),
    vusMax: valuesOf(data, 'vus_max'),
  };
  return { stdout: 'K6RESULT ' + JSON.stringify(result) + '\n' };
}
