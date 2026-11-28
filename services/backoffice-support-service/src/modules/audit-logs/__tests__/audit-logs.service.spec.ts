import { AuditLogsService } from '../audit-logs.service';

describe('AuditLogsService (P11-T04)', () => {
  let service: AuditLogsService;

  beforeEach(() => {
    service = new AuditLogsService();
  });

  it('records an audit event and masks PII in details', () => {
    const recorded = service.record({
      source: 'IAM',
      event: 'PASSWORD_CHANGED',
      actorUserId: 'user-lan',
      targetId: 'user-lan',
      outcome: 'SUCCESS',
      reason: 'User self-service password reset',
      detail: {
        email: 'lan.nguyen@example.com',
        phoneNumber: '0901234567',
        recipient: 'Lan Nguyen',
        shippingAddress: {
          line1: '123 Le Loi',
          ward: 'Ben Nghe',
          district: 'District 1',
          province: 'Ho Chi Minh City',
        },
        ip: '192.168.1.1',
      },
    });

    expect(recorded.id).toBeDefined();
    expect(recorded.detail?.email).toBe('l***@example.com');
    expect(recorded.detail?.phoneNumber).toBe('***67');
    expect(recorded.detail?.recipient).toBe('[REDACTED]');
    expect(recorded.detail?.shippingAddress).toEqual({
      line1: '[REDACTED_ADDRESS]',
      ward: '[REDACTED_ADDRESS]',
      district: '[REDACTED_ADDRESS]',
      province: '[REDACTED_ADDRESS]',
    });
    expect(recorded.detail?.ip).toBe('192.168.1.1');
  });

  it('masks spaced, dotted, dashed, bare 84, and (+84) VN phone formats in audit detail text', () => {
    const recorded = service.record({
      source: 'EXCEPTION_CASE',
      event: 'NOTE_ADDED',
      actorUserId: 'ops-1',
      outcome: 'SUCCESS',
      detail: {
        note: 'Callbacks: 0912 345 678 / 0912.345.678 / 0912-345-678 / 84912345678 / (+84) 912345678',
      },
    });

    expect(recorded.detail?.note).toBe('Callbacks: ***78 / ***78 / ***78 / ***78 / ***78');
  });

  it('lists audit logs with filters on actorId, source, and event', async () => {
    const list = await service.listAuditLogs({ source: 'APPROVAL' });
    expect(list.length).toBeGreaterThanOrEqual(1);
    expect(list[0].source).toBe('APPROVAL');
  });

  it('filters audit logs by time range', async () => {
    const now = new Date();
    const from = new Date(now.getTime() - 2000 * 1000).toISOString();
    const list = await service.listAuditLogs({ from });
    expect(list.length).toBeGreaterThanOrEqual(1);
  });
});
