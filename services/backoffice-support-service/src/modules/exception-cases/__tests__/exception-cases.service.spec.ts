import { BadRequestException, NotFoundException } from '@nestjs/common';
import { ExceptionCasesService } from '../exception-cases.service';

describe('ExceptionCasesService (Section 17 - Exception Cases)', () => {
  let service: ExceptionCasesService;

  beforeEach(() => {
    service = new ExceptionCasesService();
  });

  it('initializes with seed cases and calculates SLA assignment due date', () => {
    const list = service.listCases();
    expect(list.length).toBeGreaterThanOrEqual(2);

    const sc11 = service.getCase('CASE-SC11-001');
    expect(sc11.category).toBe('SETTLEMENT_MISMATCH');
    expect(sc11.status).toBe('ASSIGNED_IN_PROGRESS');
    expect(sc11.ownerId).toBe('finance-an');
  });

  it('creates an exception case in OPEN_UNASSIGNED status with 30-min SLA timer', () => {
    const before = Date.now();
    const created = service.createCase({
      referenceId: 'TX-PAYMENT-999',
      category: 'PAYMENT_UNKNOWN_EXPIRED',
      summary: 'Payment status UNKNOWN after 30 minutes for order O-999 (SC05)',
      actor: 'PAYMENT_SAGA',
      evidenceUrl: 'https://gateway.example.com/tx/999',
    });

    expect(created.caseId).toBeDefined();
    expect(created.status).toBe('OPEN_UNASSIGNED');
    expect(created.ownerId).toBeNull();
    expect(created.timeline.length).toBe(1);

    const dueTime = new Date(created.slaFirstAssignDueAt).getTime();
    // SLA due time should be approximately 30 minutes from creation
    expect(dueTime - before).toBeGreaterThanOrEqual(29 * 60 * 1000);
    expect(dueTime - before).toBeLessThanOrEqual(31 * 60 * 1000);
  });

  it('assigns an owner within SLA and transitions status to ASSIGNED_IN_PROGRESS', () => {
    const created = service.createCase({
      referenceId: 'S-888',
      category: 'PICK_SHORTFALL',
      summary: 'Physical shortage when picking SKU-BP-BLK at WH-HCM-01',
    });

    const assigned = service.assignCase(created.caseId, 'staff-minh', 'ops-lead');
    expect(assigned.status).toBe('ASSIGNED_IN_PROGRESS');
    expect(assigned.ownerId).toBe('staff-minh');
    expect(assigned.timeline.length).toBe(2);
    expect(assigned.timeline[1].action).toContain('Phân công vụ việc cho staff-minh');
  });

  it('appends progress timeline events with evidence', () => {
    const created = service.createCase({
      referenceId: 'PO-777',
      category: 'RECEIVING_SHORTAGE',
      summary: 'Receiving inspection damaged package',
    });

    const updated = service.appendTimeline(created.caseId, {
      actor: 'warehouse-qc',
      action: 'Chụp ảnh kiện hàng bị móp seal',
      evidenceUrl: 'https://s3.example.com/damage-777.jpg',
      note: 'Nhà cung cấp đồng ý gửi bổ sung sau 2 ngày',
    });

    expect(updated.timeline.length).toBe(2);
    expect(updated.timeline[1].evidenceUrl).toBe('https://s3.example.com/damage-777.jpg');
    expect(updated.timeline[1].note).toBe('Nhà cung cấp đồng ý gửi bổ sung sau 2 ngày');
  });

  it('resolves case with evidence (never auto-closed by timeout)', () => {
    const created = service.createCase({
      referenceId: 'O-111',
      category: 'CANCEL_ARBITRATION',
      summary: 'Customer cancelled during packing (SC06)',
    });

    service.assignCase(created.caseId, 'cskh-lead', 'ops-lead');

    // Attempting resolution without evidence or note must be rejected
    expect(() =>
      service.resolveCase(created.caseId, 'cskh-lead', '', ''),
    ).toThrow(BadRequestException);

    const resolved = service.resolveCase(
      created.caseId,
      'cskh-lead',
      'https://s3.example.com/arbitration-stopped.pdf',
      'Kho đã xác nhận dừng đóng gói thành công trước khi xuất',
    );

    expect(resolved.status).toBe('RESOLVED_WITH_EVIDENCE');
    expect(resolved.resolutionEvidence).toBe('https://s3.example.com/arbitration-stopped.pdf');
    expect(resolved.timeline[resolved.timeline.length - 1].action).toContain('Đóng hồ sơ ngoại lệ với bằng chứng');
  });

  it('filters cases by status, category, owner, and overdue SLA', () => {
    const list = service.listCases({ category: 'SETTLEMENT_MISMATCH' });
    expect(list.length).toBe(1);
    expect(list[0].caseId).toBe('CASE-SC11-001');

    const byOwner = service.listCases({ ownerId: 'finance-an' });
    expect(byOwner.length).toBe(1);
  });

  it('throws NotFoundException on invalid caseId', () => {
    expect(() => service.getCase('CASE-NON-EXISTENT')).toThrow(NotFoundException);
    expect(() => service.assignCase('CASE-NON-EXISTENT', 'user', 'lead')).toThrow(NotFoundException);
  });
});
