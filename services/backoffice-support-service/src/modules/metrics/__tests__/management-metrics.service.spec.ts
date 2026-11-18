import { ExceptionCasesService } from '../../exception-cases/exception-cases.service';
import { ManagementMetricsService } from '../management-metrics.service';

describe('ManagementMetricsService (P11-T05, SC12)', () => {
  let service: ManagementMetricsService;
  let exceptionCases: ExceptionCasesService;

  beforeEach(() => {
    exceptionCases = new ExceptionCasesService();
    service = new ManagementMetricsService(undefined, exceptionCases);
  });

  it('calculates confirmation rate and financial collections (gross - refund = net)', async () => {
    const metrics = await service.getMetrics('TODAY');

    expect(metrics.overview).toBeDefined();
    expect(metrics.overview.confirmationRate).toBeGreaterThan(0);
    expect(metrics.overview.grossCollectedVnd).toBeGreaterThan(0);
    expect(metrics.overview.totalRefundedVnd).toBeGreaterThan(0);
    expect(metrics.overview.netCollectionVnd).toBe(
      metrics.overview.grossCollectedVnd - metrics.overview.totalRefundedVnd,
    );
  });

  it('aggregates exception case aging and 30-min SLA alerts', async () => {
    const metrics = await service.getMetrics('TODAY');

    expect(metrics.caseAging).toBeDefined();
    expect(metrics.caseAging.totalOpenCases).toBeGreaterThanOrEqual(1);
    expect(metrics.caseAging.openUnder2h).toBeGreaterThanOrEqual(0);
  });

  it('includes SC12 metadata banner warning that reports never drive transactional limits', async () => {
    const metrics = await service.getMetrics('TODAY');

    expect(metrics.metadata).toBeDefined();
    expect(metrics.metadata.policyNotice).toContain('Critical Rule SC12');
    expect(metrics.metadata.source).toContain('PostgreSQL');
  });

  it('filters terminal shipments by their update time for the selected period', async () => {
    const environment = jest.replaceProperty(process, 'env', {
      ...process.env,
      ORDER_SERVICE_URL: 'http://orders',
      PAYMENT_SERVICE_URL: 'http://payments',
      FULFILLMENT_SERVICE_URL: 'http://fulfillment',
      INVENTORY_SERVICE_URL: '',
      INTERNAL_SERVICE_TOKEN: 'test-token',
    });
    const upstream = jest.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
      const address = new URL(String(url));
      const data = {
        orders: [],
        payments: { grossCollectedVnd: 690_000, totalRefundedVnd: 270_000 },
        fulfillment: [
          { status: 'DELIVERED', updatedAt: new Date().toISOString() },
          { status: 'RETURN_TO_SENDER', updatedAt: '2000-01-01T00:00:00.000Z' },
          { status: 'IN_TRANSIT', updatedAt: new Date().toISOString() },
        ],
      }[address.hostname];
      return new Response(JSON.stringify({ data }));
    });
    try {
      expect((await service.getMetrics('7D')).overview.onTimeDeliveryRate).toBe(100);
      expect((await service.getMetrics('ALL')).overview.onTimeDeliveryRate).toBe(50);
    } finally {
      upstream.mockRestore();
      environment.restore();
    }
  });

  it('queries payments.refund_obligations and IN_FULFILLMENT and propagates DB errors', async () => {
    const queries: string[] = [];
    const mockDb: any = {
      query: jest.fn(async (sql: string) => {
        queries.push(sql);
        if (sql.includes('orders.orders')) return { rows: [{ total: '4', confirmed: '3' }] };
        if (sql.includes('payments.payment_attempts')) return { rows: [{ collected: '1200000' }] };
        return { rows: [{ refunded: '270000' }] };
      }),
    };
    const dbMetrics = await new ManagementMetricsService(mockDb, exceptionCases).getMetrics('7D');
    expect(dbMetrics.overview.totalOrders).toBe(4);
    expect(dbMetrics.overview.totalRefundedVnd).toBe(270000);
    expect(queries.some((q) => q.includes('payments.refund_obligations'))).toBe(true);
    expect(queries.some((q) => q.includes('IN_FULFILLMENT'))).toBe(true);
    expect(queries.some((q) => q.includes('payments.refunds'))).toBe(false);

    const brokenDb: any = { query: jest.fn().mockRejectedValue(new Error('pg down')) };
    await expect(new ManagementMetricsService(brokenDb, exceptionCases).getMetrics('TODAY')).rejects.toThrow(
      'pg down',
    );
  });
});
