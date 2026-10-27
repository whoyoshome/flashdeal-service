// Flash-sale opening: a burst of unique buyers hits one deal at the same
// time. The run proves zero overselling and measures reservation latency,
// split into successful reservations and sold-out rejections.
//
// Parameters (environment variables):
//   BASE_URL   API base URL           (default http://api:8080)
//   STOCK      units on sale          (default 10000)
//   PEAK_RPS   peak attempts/second   (default 5000)
//   RAMP       time to reach the peak (default 2s)
//   HOLD       time held at the peak  (default 20s)
//
// The defaults model an opening burst: the peak arrives in seconds and the
// stock sells out under contention, not during a gentle ramp. Iterations k6
// cannot start because every VU is still waiting are reported as
// dropped_iterations: offered load the service did not absorb.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://api:8080';
const STOCK = Number(__ENV.STOCK || 10000);
const PEAK_RPS = Number(__ENV.PEAK_RPS || 5000);
const RAMP = __ENV.RAMP || '2s';
const HOLD = __ENV.HOLD || '20s';

// 201 (reserved) and 409 (sold out) are both correct answers under load;
// only other statuses count as failed requests.
http.setResponseCallback(http.expectedStatuses(201, 409));

const reserved = new Counter('reservations_created');
const soldOut = new Counter('reservations_sold_out');
// Overload makes requests exceed their time budget (503 REQUEST_TIMEOUT).
// That is degraded availability, not a correctness violation.
const timedOut = new Rate('reservations_timed_out');
const serverErrors = new Rate('server_errors');
const unexpected = new Rate('unexpected_responses');
const reservedLatency = new Trend('reserve_ok_duration', true);
const soldOutLatency = new Trend('reserve_sold_out_duration', true);

export const options = {
  scenarios: {
    opening: {
      executor: 'ramping-arrival-rate',
      startRate: 0,
      timeUnit: '1s',
      preAllocatedVUs: 200,
      maxVUs: 2000,
      stages: [
        { target: PEAK_RPS, duration: RAMP },
        { target: PEAK_RPS, duration: HOLD },
      ],
    },
  },
  thresholds: {
    // Correctness: never more reservations than stock, and no answer other
    // than 201, 409 SOLD_OUT or 503 REQUEST_TIMEOUT.
    reservations_created: [`count<=${STOCK}`],
    unexpected_responses: ['rate==0'],
    // The non-functional targets in docs/ARCHITECTURE.md. A run that misses
    // them fails, so a regression cannot pass silently.
    reserve_ok_duration: ['p(99)<50'],
    reserve_sold_out_duration: ['p(99)<10'],
    server_errors: ['rate<0.01'],
  },
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'p(99)', 'max'],
};

export function setup() {
  const now = Date.now();
  const deal = {
    product_id: `load-${now}`,
    title: 'k6 flash sale',
    price_cents: 4999,
    currency: 'USD',
    total_stock: STOCK,
    per_user_limit: 1,
    starts_at: new Date(now - 60_000).toISOString(),
    ends_at: new Date(now + 3_600_000).toISOString(),
  };
  const res = http.post(`${BASE_URL}/v1/deals`, JSON.stringify(deal), {
    headers: { 'Content-Type': 'application/json' },
    responseCallback: http.expectedStatuses(201),
  });
  if (res.status !== 201) {
    throw new Error(`create deal: HTTP ${res.status} ${res.body}`);
  }
  const id = res.json('id');
  const act = http.post(`${BASE_URL}/v1/deals/${id}/activate`, null, {
    responseCallback: http.expectedStatuses(200),
  });
  if (act.status !== 200) {
    throw new Error(`activate deal: HTTP ${act.status} ${act.body}`);
  }
  // runId keeps buyers and idempotency keys unique across runs; reusing a
  // key for another deal is correctly rejected with 409 CONFLICT.
  return { dealId: id, runId: now };
}

export default function (data) {
  // Every iteration is a distinct buyer, so the per-user limit never
  // interferes: only stock decides the outcome.
  const buyer = `${data.runId}-vu${__VU}-it${__ITER}`;
  const res = http.post(
    `${BASE_URL}/v1/deals/${data.dealId}/reservations`,
    JSON.stringify({ quantity: 1 }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-User-ID': buyer,
        'Idempotency-Key': `${buyer}-key`,
      },
      tags: { name: 'reserve' },
    },
  );

  const ok = res.status === 201;
  const isSoldOut = res.status === 409 && res.json('code') === 'SOLD_OUT';
  const isTimeout = res.status === 503 && res.json('code') === 'REQUEST_TIMEOUT';
  unexpected.add(!ok && !isSoldOut && !isTimeout);
  timedOut.add(isTimeout);
  serverErrors.add(res.status >= 500);
  if (ok) {
    reserved.add(1);
    reservedLatency.add(res.timings.duration);
  } else if (isSoldOut) {
    soldOut.add(1);
    soldOutLatency.add(res.timings.duration);
  }
  check(res, { 'reserved, sold out or timed out': () => ok || isSoldOut || isTimeout });
}

// teardown reads the deal back: after the burst, every unit must be held
// and none may be oversold. Single-deal reads are cached for a few seconds
// (DEAL_CACHE_TTL), so it polls until the snapshot catches up.
export function teardown(data) {
  let deal;
  for (let attempt = 0; attempt < 20; attempt++) {
    deal = http.get(`${BASE_URL}/v1/deals/${data.dealId}`, {
      responseCallback: http.expectedStatuses(200),
    }).json();
    if (deal.available_stock === 0) {
      break;
    }
    sleep(0.5);
  }
  console.log(
    `deal ${data.dealId}: total_stock=${deal.total_stock} available_stock=${deal.available_stock}`,
  );
  check(deal, {
    'no unit oversold': (d) => d.available_stock >= 0,
    'stock fully reserved': (d) => d.available_stock === 0,
  });
}
