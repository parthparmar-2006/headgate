import http from 'k6/http';
import exec from 'k6/execution';
import { check } from 'k6';

const targets = __ENV.TARGETS.split(',');
const upstreamURL = __ENV.UPSTREAM_URL;
const limitPerWindow = parseInt(__ENV.RATE_LIMIT, 10);
const keyCount = parseInt(__ENV.KEY_COUNT || '5', 10);
const ratePerKey = parseInt(__ENV.RATE_PER_KEY || '600', 10);
const duration = __ENV.DURATION || '30s';
const windowMs = parseInt(__ENV.WINDOW_MS || '1000', 10);
const edgeWindowsToSkip = 2;

http.setResponseCallback(http.expectedStatuses(200, 429));

export const options = {
  scenarios: {
    overshoot: {
      executor: 'constant-arrival-rate',
      rate: keyCount * ratePerKey,
      timeUnit: '1s',
      duration: duration,
      preAllocatedVUs: 200,
      maxVUs: 1000,
    },
  },
};

export function setup() {
  http.post(`${upstreamURL}/_reset`);
}

export default function () {
  const iteration = exec.scenario.iterationInTest;
  const target = targets[iteration % targets.length];
  const keyIndex = Math.floor(iteration / targets.length) % keyCount;
  const apiKey = `key-${keyIndex}`;

  const response = http.get(`${target}/resource`, { headers: { 'X-API-Key': apiKey } });
  check(response, {
    'status is 200 or 429': (r) => r.status === 200 || r.status === 429,
  });
}

export function teardown() {
  const stats = http.get(`${upstreamURL}/_stats`).json();
  const worstRolling = http.get(`${upstreamURL}/_rolling?window_ms=${windowMs}`).json();
  const result = {
    limitPerWindow,
    replicas: targets.length,
    worstRolling,
    byDecision: reportFor(stats.decision),
    byArrival: reportFor(stats.arrival),
  };
  console.log(JSON.stringify(result, null, 2));
}

function reportFor(countsByKey) {
  const report = [];
  for (const apiKey of Object.keys(countsByKey).sort()) {
    const counts = fullWindowCounts(countsByKey[apiKey]);
    report.push(summarize(apiKey, counts));
  }
  return report;
}

function fullWindowCounts(perSecond) {
  const seconds = Object.keys(perSecond).map(Number).sort((a, b) => a - b);
  const inner = seconds.slice(edgeWindowsToSkip, seconds.length - edgeWindowsToSkip);
  return inner.map((second) => perSecond[String(second)]);
}

function summarize(apiKey, counts) {
  let total = 0;
  let min = Infinity;
  let max = 0;
  let windowsOverLimit = 0;
  for (const count of counts) {
    total += count;
    min = Math.min(min, count);
    max = Math.max(max, count);
    if (count > limitPerWindow) {
      windowsOverLimit++;
    }
  }
  const mean = total / counts.length;
  return {
    apiKey,
    windows: counts.length,
    min,
    mean: Number(mean.toFixed(2)),
    max,
    windowsOverLimit,
    meanOverLimit: Number((mean / limitPerWindow).toFixed(4)),
  };
}
