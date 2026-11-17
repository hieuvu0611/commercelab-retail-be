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
});
