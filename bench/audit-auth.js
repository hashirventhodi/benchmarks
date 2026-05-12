import http from 'k6/http';
import { check } from 'k6';

export const options = {
  scenarios: {
    audit: {
      executor: 'constant-vus',
      vus: parseInt(__ENV.VUS || '100'),
      duration: __ENV.DURATION || '30s',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
  },
};

const URL = __ENV.URL;
// 10 seeded tokens; round-robin across VUs to vary the lookup.
const TOKENS = [
  'bench-token-1', 'bench-token-2', 'bench-token-3', 'bench-token-4', 'bench-token-5',
  'bench-token-6', 'bench-token-7', 'bench-token-8', 'bench-token-9', 'bench-token-10',
];

export default function () {
  const token = TOKENS[__VU % TOKENS.length];
  const payload = JSON.stringify({
    kind: 'agent.step',
    payload: { vu: __VU, iter: __ITER, ts: Date.now() },
  });
  const res = http.post(URL, payload, {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
  });
  check(res, { '2xx': (r) => r.status >= 200 && r.status < 300 });
}
