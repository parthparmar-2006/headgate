import http from 'k6/http';
import exec from 'k6/execution';
import { check } from 'k6';

const targets = __ENV.TARGETS.split(',');
const upstreamURL = __ENV.UPSTREAM_URL;
const limitPerWindow = parseInt(__ENV.RATE_LIMIT, 10);
const keyCount = parseInt(__ENV.KEY_COUNT || '5', 10);
const ratePerKey = parseInt(__ENV.RATE_PER_KEY || '600', 10);
const duration = __ENV.DURATION || '30s';
const edgeWindowsToSkip = 2;

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
  const report = [];

  for (const apiKey of Object.keys(stats).sort()) {
    const counts = fullWindowCounts(stats[apiKey]);
    report.push(summarize(apiKey, counts));
  }

  console.log(JSON.stringify({ limitPerWindow, replicas: targets.length, report }, null, 2));
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
  for (const count of counts) {
    total += count;
    min = Math.min(min, count);
    max = Math.max(max, count);
  }
  const mean = total / counts.length;
  return {
    apiKey,
    windows: counts.length,
    min,
    mean: Number(mean.toFixed(1)),
    max,
    meanOverLimit: Number((mean / limitPerWindow).toFixed(3)),
  };
}
