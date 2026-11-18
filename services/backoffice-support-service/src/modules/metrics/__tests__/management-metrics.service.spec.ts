import { ServiceUnavailableException } from '@nestjs/common';
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

  it('aggregates period metrics including zero totals over HTTP without cross-schema SQL', async () => {
    const environment = jest.replaceProperty(process, 'env', {
      ...process.env,
      ORDER_SERVICE_URL: 'http://orders',
      PAYMENT_SERVICE_URL: 'http://payments',
      FULFILLMENT_SERVICE_URL: 'http://fulfillment',
      INVENTORY_SERVICE_URL: 'http://inventory',
      INTERNAL_SERVICE_TOKEN: 'test-token',
    });
    const upstream = jest.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
      const address = new URL(String(url));
      const data = {
        orders: [{ orderId: 'O-1', status: 'CONFIRMED', createdAt: new Date().toISOString() }],
        payments: { grossCollectedVnd: 0, totalRefundedVnd: 0 },
        fulfillment: [],
        inventory: [{ availableQty: 15, reservedQty: 2 }],
      }[address.hostname];
      return new Response(JSON.stringify({ data }));
    });
    const mockDb: any = { query: jest.fn() };
    try {
      const result = await new ManagementMetricsService(mockDb, exceptionCases).getMetrics('TODAY');
      expect(result.overview.grossCollectedVnd).toBe(0);
      expect(result.overview.totalRefundedVnd).toBe(0);
      expect(result.overview.netCollectionVnd).toBe(0);
      expect(result.overview.availableInventoryUnits).toBe(15);
      expect(result.overview.reservedInventoryUnits).toBe(2);
      expect(mockDb.query).not.toHaveBeenCalled();
    } finally {
      upstream.mockRestore();
      environment.restore();
    }
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

  it('rejects missing service URLs with 503 instead of querying orders/payments schemas directly', async () => {
    const environment = jest.replaceProperty(process, 'env', {
      ...process.env,
      ORDER_SERVICE_URL: '',
      PAYMENT_SERVICE_URL: '',
    });
    const mockDb: any = { query: jest.fn() };
    try {
      await expect(new ManagementMetricsService(mockDb, exceptionCases).getMetrics('7D')).rejects.toThrow(
        ServiceUnavailableException,
      );
      expect(mockDb.query).not.toHaveBeenCalled();
    } finally {
      environment.restore();
    }
  });

  it('surfaces 503 ServiceUnavailableException when an upstream HTTP dependency fails or is unreachable', async () => {
    const environment = jest.replaceProperty(process, 'env', {
      ...process.env,
      ORDER_SERVICE_URL: 'http://orders',
      PAYMENT_SERVICE_URL: 'http://payments',
      FULFILLMENT_SERVICE_URL: '',
      INVENTORY_SERVICE_URL: '',
      INTERNAL_SERVICE_TOKEN: 'test-token',
    });
    const upstream = jest.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
      if (String(url).includes('payments')) {
        throw new Error('connect ECONNREFUSED 127.0.0.1:8084');
      }
      return new Response(JSON.stringify({ data: [] }));
    });
    try {
      await expect(service.getMetrics('TODAY')).rejects.toThrow(
        'Upstream payment metrics unreachable: connect ECONNREFUSED 127.0.0.1:8084',
      );
    } finally {
      upstream.mockRestore();
      environment.restore();
    }
  });
});
