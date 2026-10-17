import http from 'k6/http';
import exec from 'k6/execution';
import { Counter } from 'k6/metrics';

const targets = __ENV.TARGETS.split(',');
const duration = __ENV.DURATION || '45s';
const routes = ['open', 'closed'];
const normalKeyCount = 20;
const slotsPerRoute = 3;

const degraded = new Counter('limiter_degraded');

http.setResponseCallback(http.expectedStatuses(200, 429, 503));

export const options = {
  scenarios: {
    failover: {
      executor: 'constant-arrival-rate',
      rate: 1200,
      timeUnit: '1s',
      duration: duration,
      preAllocatedVUs: 200,
      maxVUs: 1000,
    },
  },
};

export default function () {
  const iteration = exec.scenario.iterationInTest;
  const target = targets[iteration % targets.length];
  const route = routes[Math.floor(iteration / 3) % routes.length];
  const slot = Math.floor(iteration / 6) % slotsPerRoute;

  let apiKey = 'hot';
  let keyType = 'hot';
  if (slot !== 0) {
    apiKey = `normal-${Math.floor(iteration / 18) % normalKeyCount}`;
    keyType = 'normal';
  }

  const tags = { route: route, keytype: keyType };
  const response = http.get(`${target}/${route}/resource`, {
    headers: { 'X-API-Key': apiKey },
    tags: tags,
    timeout: '10s',
  });

  if (response.headers['X-Headgate-Limiter']) {
    degraded.add(1, tags);
  }
}
