import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:38080/api/v1';

export const options = {
  scenarios: {
    browse_and_checkout: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.TARGET_RPS || 500),
      timeUnit: '1s',
      duration: __ENV.DURATION || '5m',
      preAllocatedVUs: 32,
      maxVUs: 128,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.001'],
    http_req_duration: ['p(95)<300', 'p(99)<800'],
  },
};

export default function () {
  const slot = __ITER % 10;
  if (slot < 7) {
    const path = __ITER % 2 === 0
      ? `${BASE_URL}/products?q=backpack&limit=10`
      : `${BASE_URL}/inventory/availability?skuIds=SKU-BOTTLE-BLUE-500ML&warehouse=ALL`;
    const res = http.get(path);
    check(res, { 'read status 200': (r) => r.status === 200 });
  } else {
    const res = http.get(`${BASE_URL}/promotions/active`);
    check(res, { 'promo status 200': (r) => r.status === 200 });
  }
}
