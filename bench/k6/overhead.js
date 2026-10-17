import http from 'k6/http';
import exec from 'k6/execution';

const targets = __ENV.TARGETS.split(',');
const upstreamURL = __ENV.UPSTREAM_URL;
const rate = parseInt(__ENV.RATE || '1000', 10);
const duration = __ENV.DURATION || '30s';
const keyCount = 100;

export const options = {
  summaryTrendStats: ['p(50)', 'p(90)', 'p(99)', 'max'],
  scenarios: {
    via_gateway: {
      executor: 'constant-arrival-rate',
      rate: rate,
      timeUnit: '1s',
      duration: duration,
      preAllocatedVUs: 200,
      maxVUs: 1000,
      exec: 'viaGateway',
    },
    direct: {
      executor: 'constant-arrival-rate',
      rate: rate,
      timeUnit: '1s',
      duration: duration,
      preAllocatedVUs: 200,
      maxVUs: 1000,
      exec: 'direct',
    },
  },
  thresholds: {
    'http_req_duration{scenario:via_gateway}': ['max>=0'],
    'http_req_duration{scenario:direct}': ['max>=0'],
    'http_req_failed{scenario:via_gateway}': ['rate>=0'],
    'dropped_iterations{scenario:via_gateway}': ['count>=0'],
    'dropped_iterations{scenario:direct}': ['count>=0'],
  },
};

function apiKey(iteration) {
  return `bench-${iteration % keyCount}`;
}

export function viaGateway() {
  const iteration = exec.scenario.iterationInTest;
  const target = targets[iteration % targets.length];
  http.get(`${target}/bench/resource`, { headers: { 'X-API-Key': apiKey(iteration) } });
}

export function direct() {
  const iteration = exec.scenario.iterationInTest;
  http.get(`${upstreamURL}/resource`, { headers: { 'X-API-Key': apiKey(iteration) } });
}

function pick(data, name) {
  const metric = data.metrics[name];
  if (!metric) {
    return null;
  }
  return metric.values;
}

export function handleSummary(data) {
  const result = {
    rate: rate,
    viaGateway: pick(data, 'http_req_duration{scenario:via_gateway}'),
    direct: pick(data, 'http_req_duration{scenario:direct}'),
    gatewayFailed: pick(data, 'http_req_failed{scenario:via_gateway}'),
    droppedViaGateway: pick(data, 'dropped_iterations{scenario:via_gateway}'),
    droppedDirect: pick(data, 'dropped_iterations{scenario:direct}'),
  };
  return { stdout: 'K6RESULT ' + JSON.stringify(result) + '\n' };
}
