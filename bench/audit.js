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
const ORG = '00000000-0000-0000-0000-000000000001';
const ACTOR = '00000000-0000-0000-0000-000000000002';

export default function () {
  const payload = JSON.stringify({
    kind: 'agent.step',
    org_id: ORG,
    actor_id: ACTOR,
    payload: { vu: __VU, iter: __ITER, ts: Date.now() },
  });
  const res = http.post(URL, payload, {
    headers: { 'Content-Type': 'application/json' },
  });
  check(res, { '2xx': (r) => r.status >= 200 && r.status < 300 });
}
