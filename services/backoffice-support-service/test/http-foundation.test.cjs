require('reflect-metadata');
const { after, before, test } = require('node:test');
const assert = require('node:assert/strict');
const jwt = require('jsonwebtoken');

process.env.JWT_ACCESS_SECRET =
  process.env.JWT_ACCESS_SECRET || 'foundation-test-jwt-secret-min-32-bytes';
process.env.INTERNAL_SERVICE_TOKEN =
  process.env.INTERNAL_SERVICE_TOKEN || 'foundation-test-internal-secret-32b';

const { NestFactory } = require('@nestjs/core');
const { AppModule } = require('../dist/app.module');
const { configureHttpFoundation } = require('../dist/common/http/http-foundation');

const testBearerToken = jwt.sign(
  { sub: 'cust-foundation', roles: ['CUSTOMER'], warehouses: [] },
  process.env.JWT_ACCESS_SECRET,
);

let app;
let base;
before(async () => {
  app = await NestFactory.create(AppModule, { logger: false });
  configureHttpFoundation(app);
  await app.listen(0, '127.0.0.1');
  base = await app.getUrl();
});
after(async () => { if (app) await app.close(); });

test('preserves the success envelope', async () => {
  const res = await fetch(`${base}/health`);
  const body = await res.json();
  assert.equal(res.status, 200);
  assert.equal(body.message, 'backoffice-support-service healthy');
  assert.equal(body.data.service, 'backoffice-support-service');
  assert.equal(body.data.data, undefined);
});

test('validates DTOs and returns the common error structure', async () => {
  const res = await fetch(`${base}/api/v1/returns`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      authorization: `Bearer ${testBearerToken}`,
      'x-correlation-id': 'req-test',
    },
    body: JSON.stringify({ orderId: '', reason: 'INVALID', lines: [] }),
  });
  const body = await res.json();
  assert.equal(res.status, 422);
  assert.equal(body.code, 'VALIDATION_FAILED');
  assert.equal(body.correlationId, 'req-test');
  assert.ok(body.errors.orderId && body.errors.lines && body.errors.reason);
  assert.equal(res.headers.get('x-correlation-id'), body.correlationId);
});

test('normalizes framework 404 errors and generates correlation ID', async () => {
  const res = await fetch(`${base}/not-found`);
  const body = await res.json();
  assert.equal(res.status, 404);
  assert.equal(body.code, 'NOT_FOUND');
  assert.match(body.correlationId, /^req-[a-f0-9]{16}$/);
  assert.equal(res.headers.get('x-correlation-id'), body.correlationId);
});

test('returns a common error for malformed JSON', async () => {
  const res = await fetch(`${base}/api/v1/returns`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      authorization: `Bearer ${testBearerToken}`,
    },
    body: '{',
  });
  const body = await res.json();
  assert.equal(res.status, 400);
  assert.equal(body.code, 'INVALID_REQUEST');
  assert.ok(body.correlationId);
});

test('surfaces 503 SERVICE_UNAVAILABLE message when metrics upstreams are unconfigured', async () => {
  const staffToken = jwt.sign(
    { sub: 'ops-foundation', roles: ['OPS_MGR'], warehouses: ['ALL'] },
    process.env.JWT_ACCESS_SECRET,
  );
  const res = await fetch(`${base}/api/v1/backoffice/metrics?period=TODAY`, {
    headers: {
      authorization: `Bearer ${staffToken}`,
      'x-correlation-id': 'req-metrics-503',
    },
  });
  const body = await res.json();
  assert.equal(res.status, 503);
  assert.equal(body.code, 'SERVICE_UNAVAILABLE');
  assert.match(body.message, /ORDER_SERVICE_URL/);
  assert.equal(body.correlationId, 'req-metrics-503');
});
