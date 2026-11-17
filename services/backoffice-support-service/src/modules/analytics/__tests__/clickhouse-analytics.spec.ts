import { ClickhouseAnalyticsService } from '../clickhouse-analytics.service';

describe('ClickhouseAnalyticsService (SC12 OLAP)', () => {
  const origEnv = process.env;
  const origFetch = global.fetch;

  beforeEach(() => {
    process.env = {
      ...origEnv,
      CLICKHOUSE_URL: 'http://clickhouse.test:8123',
      CLICKHOUSE_DB: 'commerce_analytics',
      ORDER_SERVICE_URL: 'http://orders.test:8083',
      INVENTORY_SERVICE_URL: 'http://inventory.test:8082',
      FULFILLMENT_SERVICE_URL: 'http://fulfillment.test:8085',
    };
  });

  afterEach(() => {
    process.env = origEnv;
    global.fetch = origFetch;
    jest.restoreAllMocks();
  });

  it('syncs orders/shipments/stock into ClickHouse and queries OLAP KPIs with SC12 lag metadata', async () => {
    const chCalls: string[] = [];
    global.fetch = jest.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/api/v1/orders/internal')) {
        return new Response(
          JSON.stringify({
            data: [
              { orderId: 'O-1', status: 'DELIVERED', paymentMethod: 'PREPAID', totalPayableVnd: 660000, discountVnd: 70000 },
              { orderId: 'O-2', status: 'CONFIRMED', paymentMethod: 'COD', totalPayableVnd: 450000, discountVnd: 0 },
            ],
          }),
          { status: 200 },
        );
      }
      if (url.includes('/api/v1/shipments/internal')) {
        return new Response(
          JSON.stringify({
            data: [
              { shipmentId: 'SHP-O-1', orderId: 'O-1', warehouseId: 'WH-HCM-01', status: 'DELIVERED', codAmountToCollect: 0 },
              { shipmentId: 'SHP-O-2-2', orderId: 'O-2', warehouseId: 'WH-HN-01', status: 'IN_TRANSIT', codAmountToCollect: 450000 },
            ],
          }),
          { status: 200 },
        );
      }
      if (url.includes('/api/v1/inventory/internal/stock')) {
        return new Response(
          JSON.stringify({
            data: [{ skuId: 'SKU-1', warehouseId: 'WH-HCM-01', availableQty: 12, reservedQty: 2 }],
          }),
          { status: 200 },
        );
      }
      chCalls.push(decodeURIComponent(url));
      if (url.includes('FROM%20order_facts') && url.includes('count()')) {
        return new Response(
          JSON.stringify({
            data: [{ totalOrders: 2, confirmedOrders: 2, deliveredOrders: 1, grossGmvVnd: 1110000, totalDiscountVnd: 70000, codOrders: 1, prepaidOrders: 1 }],
          }),
          { status: 200 },
        );
      }
      if (url.includes('GROUP%20BY%20status')) {
        return new Response(JSON.stringify({ data: [{ status: 'DELIVERED', orders: 1, gmvVnd: 660000 }] }), { status: 200 });
      }
      if (url.includes('FROM%20shipment_facts')) {
        return new Response(JSON.stringify({ data: [{ warehouseId: 'WH-HCM-01', shipments: 1, delivered: 1, codToCollectVnd: 0 }] }), { status: 200 });
      }
      if (url.includes('FROM%20warehouse_inventory_facts')) {
        return new Response(JSON.stringify({ data: [{ warehouseId: 'WH-HCM-01', availableUnits: 12, reservedUnits: 2 }] }), { status: 200 });
      }
      if (url.includes('FROM%20sync_watermarks')) {
        return new Response(JSON.stringify({ data: [{ lastSyncedAt: '2026-11-17 15:00:00.000' }] }), { status: 200 });
      }
      return new Response('', { status: 200 });
    }) as any;

    const svc = new ClickhouseAnalyticsService();
    const syncRes = await svc.syncFromSources('Bearer test-token');
    expect(syncRes.ordersSynced).toBe(2);
    expect(syncRes.shipmentsSynced).toBe(2);
    expect(syncRes.inventoryRowsSynced).toBe(1);
    expect(chCalls.some((c) => c.includes('INSERT INTO order_facts'))).toBe(true);

    const dash = await svc.getOlapDashboard('30D');
    expect(dash.kpis.totalOrders).toBe(2);
    expect(dash.kpis.grossGmvVnd).toBe(1110000);
    expect(dash.byWarehouse[0].warehouseId).toBe('WH-HCM-01');
    expect(dash.metadata.source).toBe('ClickHouse OLAP');
    expect(dash.metadata.policyNotice).toContain('SC12');
    expect(dash.metadata.lastSyncedAt).toBeTruthy();
  });
});
